package capture

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
	"unicode/utf8"
)

// ErrNoTranscript means the body carried nothing worth archiving (an image-only
// request, an empty poll, an unparseable shape). It is not a failure.
var ErrNoTranscript = errors.New("capture: no transcript in request")

// MaxTranscriptRunes bounds one archived conversation. It is a defence against
// a pathological request, not a content policy: the largest conversation seen
// in production is ~760k runes, so this leaves an order of magnitude of room.
//
// The archive exists to reconstruct a conversation. Cutting one in half to save
// space defeats the purpose, which is what the 64 KiB limit inherited from the
// Guard scanner did.
const MaxTranscriptRunes = 4 << 20

// turnHashLen is how much of each turn's SHA-256 is kept in the chain. 64 bits
// is far more than enough to tell turns apart inside one user's conversations,
// and keeps the chain from dominating the payload.
const turnHashLen = 16

// Turn is one entry of a conversation, in the order the client sent it.
type Turn struct {
	// Role is user / assistant / system / tool_call / tool_result / reasoning.
	Role string `json:"role"`
	// Name is the tool name on tool_call and tool_result turns.
	Name string `json:"name,omitempty"`
	Text string `json:"text"`
}

// Transcript is a conversation as one request carried it.
//
// Because these APIs are stateless, every request replays the whole
// conversation so far. One transcript is therefore a complete snapshot, and the
// last request of a conversation reconstructs all of it — provided nothing is
// dropped or reordered on the way in, which is exactly what this extractor is
// for.
type Transcript struct {
	Turns []Turn
	// Text is the rendered conversation, in chronological order.
	Text string
	// Chain holds one short hash per turn, in order. The consumer uses it to
	// recognise that a new request continues a conversation it already has:
	// the stored chain is a prefix of the new one.
	Chain     []string
	Hash      string
	Chars     int
	Truncated bool
}

// ExtractTranscript turns a raw gateway request body into a conversation.
//
// It deliberately does not reuse securityaudit's extractor. That one serves a
// content scanner: it keeps only human-readable text, drops every tool item,
// and reorders turns to put the most suspicious content first. All three are
// right for scanning and wrong for an archive.
func ExtractTranscript(protocol string, body []byte) (Transcript, error) {
	var root map[string]any
	if err := json.Unmarshal(body, &root); err != nil {
		return Transcript{}, ErrNoTranscript
	}

	var turns []Turn
	switch normaliseProtocol(protocol) {
	case "chat":
		turns = chatTurns(root)
	case "anthropic":
		turns = anthropicTurns(root)
	case "responses":
		turns = responsesTurns(root)
	case "gemini":
		turns = geminiTurns(root)
	default:
		turns = genericTurns(root)
	}

	turns = compactTurns(turns)
	if len(turns) == 0 {
		return Transcript{}, ErrNoTranscript
	}
	return buildTranscript(turns), nil
}

func normaliseProtocol(protocol string) string {
	switch strings.ToLower(strings.TrimSpace(protocol)) {
	case "openai_chat_completions", "openai_chat", "chat_completions":
		return "chat"
	case "anthropic_messages", "claude_messages", "messages":
		return "anthropic"
	case "openai_responses", "responses", "responses_websocket":
		return "responses"
	case "gemini", "gemini_generate_content":
		return "gemini"
	default:
		return "generic"
	}
}

func buildTranscript(turns []Turn) Transcript {
	var b strings.Builder
	chain := make([]string, 0, len(turns))
	for _, t := range turns {
		chain = append(chain, turnHash(t))
		b.WriteString(renderTurn(t))
	}
	text := b.String()

	truncated := false
	if utf8.RuneCountInString(text) > MaxTranscriptRunes {
		runes := []rune(text)
		text = string(runes[:MaxTranscriptRunes]) + "\n\n[... truncated by the archive ...]"
		truncated = true
	}
	sum := sha256.Sum256([]byte(text))
	return Transcript{
		Turns:     turns,
		Text:      text,
		Chain:     chain,
		Hash:      hex.EncodeToString(sum[:]),
		Chars:     utf8.RuneCountInString(text),
		Truncated: truncated,
	}
}

func renderTurn(t Turn) string {
	head := t.Role
	if t.Name != "" {
		head += ": " + t.Name
	}
	return "[" + head + "]\n" + t.Text + "\n\n"
}

func turnHash(t Turn) string {
	sum := sha256.Sum256([]byte(t.Role + "\x00" + t.Name + "\x00" + t.Text))
	return hex.EncodeToString(sum[:])[:turnHashLen]
}

// compactTurns drops empty turns while preserving order.
func compactTurns(turns []Turn) []Turn {
	out := make([]Turn, 0, len(turns))
	for _, t := range turns {
		t.Text = strings.TrimSpace(t.Text)
		if t.Text == "" && t.Name == "" {
			continue
		}
		if t.Role == "" {
			t.Role = "user"
		}
		out = append(out, t)
	}
	return out
}

// ── OpenAI Chat Completions ────────────────────────────────────────────────

func chatTurns(root map[string]any) []Turn {
	var out []Turn
	for _, raw := range arrayOf(root["messages"]) {
		msg, ok := raw.(map[string]any)
		if !ok {
			continue
		}
		role := strings.ToLower(stringOf(msg["role"]))
		if role == "tool" {
			out = append(out, Turn{
				Role: "tool_result",
				Name: stringOf(msg["tool_call_id"]),
				Text: joinTexts(contentStrings(msg["content"])),
			})
			continue
		}
		if reasoning := stringOf(msg["reasoning_content"]); reasoning != "" {
			out = append(out, Turn{Role: "reasoning", Text: reasoning})
		}
		if text := joinTexts(contentStrings(msg["content"])); text != "" {
			out = append(out, Turn{Role: role, Text: text})
		}
		for _, rawCall := range arrayOf(msg["tool_calls"]) {
			call, ok := rawCall.(map[string]any)
			if !ok {
				continue
			}
			fn, _ := call["function"].(map[string]any)
			name, args := "", ""
			if fn != nil {
				name, args = stringOf(fn["name"]), stringOf(fn["arguments"])
			}
			out = append(out, Turn{Role: "tool_call", Name: name, Text: args})
		}
	}
	return out
}

// ── Anthropic Messages ─────────────────────────────────────────────────────

func anthropicTurns(root map[string]any) []Turn {
	var out []Turn
	if system := joinTexts(contentStrings(root["system"])); system != "" {
		out = append(out, Turn{Role: "system", Text: system})
	}
	for _, raw := range arrayOf(root["messages"]) {
		msg, ok := raw.(map[string]any)
		if !ok {
			continue
		}
		role := strings.ToLower(stringOf(msg["role"]))
		switch content := msg["content"].(type) {
		case string:
			out = append(out, Turn{Role: role, Text: content})
		case []any:
			out = append(out, anthropicBlocks(role, content)...)
		}
	}
	return out
}

func anthropicBlocks(role string, blocks []any) []Turn {
	var out []Turn
	for _, raw := range blocks {
		block, ok := raw.(map[string]any)
		if !ok {
			continue
		}
		switch strings.ToLower(stringOf(block["type"])) {
		case "text":
			out = append(out, Turn{Role: role, Text: stringOf(block["text"])})
		case "thinking":
			out = append(out, Turn{Role: "reasoning", Text: stringOf(block["thinking"])})
		case "tool_use":
			out = append(out, Turn{
				Role: "tool_call",
				Name: stringOf(block["name"]),
				Text: jsonText(block["input"]),
			})
		case "tool_result":
			out = append(out, Turn{
				Role: "tool_result",
				Name: stringOf(block["tool_use_id"]),
				Text: joinTexts(contentStrings(block["content"])),
			})
		}
	}
	return out
}

// ── OpenAI Responses ───────────────────────────────────────────────────────

func responsesTurns(root map[string]any) []Turn {
	// A WebSocket frame wraps the request one level down.
	if frame := stringOf(root["type"]); frame != "" {
		if frame != "response.create" {
			return nil
		}
		if inner, ok := root["response"].(map[string]any); ok {
			root = inner
		}
	}

	var out []Turn
	if instructions := joinTexts(contentStrings(root["instructions"])); instructions != "" {
		out = append(out, Turn{Role: "system", Text: instructions})
	}
	switch input := root["input"].(type) {
	case string:
		out = append(out, Turn{Role: "user", Text: input})
	case []any:
		out = append(out, responsesItems(input)...)
	}
	return out
}

func responsesItems(items []any) []Turn {
	var out []Turn
	for _, raw := range items {
		switch item := raw.(type) {
		case string:
			out = append(out, Turn{Role: "user", Text: item})
		case map[string]any:
			out = append(out, responsesItem(item)...)
		}
	}
	return out
}

func responsesItem(item map[string]any) []Turn {
	itemType := strings.ToLower(stringOf(item["type"]))
	switch itemType {
	case "function_call", "custom_tool_call":
		// The item a scanner-oriented extractor drops: no role, no content,
		// and the single most interesting thing an agent does.
		return []Turn{{
			Role: "tool_call",
			Name: stringOf(item["name"]),
			Text: firstNonEmpty(stringOf(item["arguments"]), stringOf(item["input"])),
		}}
	case "function_call_output", "custom_tool_call_output":
		return []Turn{{
			Role: "tool_result",
			Name: stringOf(item["call_id"]),
			Text: firstNonEmpty(joinTexts(contentStrings(item["output"])), jsonText(item["output"])),
		}}
	case "reasoning":
		// Only the plaintext summary is archived; encrypted_content is opaque
		// ciphertext that would bloat the record without being readable.
		return []Turn{{Role: "reasoning", Text: joinTexts(contentStrings(item["summary"]))}}
	}

	role := strings.ToLower(stringOf(item["role"]))
	if role == "" {
		role = "user"
	}
	if text := joinTexts(contentStrings(item["content"])); text != "" {
		return []Turn{{Role: role, Text: text}}
	}
	if text := stringOf(item["text"]); text != "" {
		return []Turn{{Role: role, Text: text}}
	}
	return nil
}

// ── Gemini ─────────────────────────────────────────────────────────────────

func geminiTurns(root map[string]any) []Turn {
	var out []Turn
	if sys, ok := root["systemInstruction"].(map[string]any); ok {
		if text := joinTexts(contentStrings(sys["parts"])); text != "" {
			out = append(out, Turn{Role: "system", Text: text})
		}
	}
	for _, raw := range arrayOf(root["contents"]) {
		content, ok := raw.(map[string]any)
		if !ok {
			continue
		}
		role := strings.ToLower(stringOf(content["role"]))
		if role == "model" {
			role = "assistant"
		}
		for _, rawPart := range arrayOf(content["parts"]) {
			part, ok := rawPart.(map[string]any)
			if !ok {
				continue
			}
			if text := stringOf(part["text"]); text != "" {
				out = append(out, Turn{Role: role, Text: text})
			}
			if call, ok := part["functionCall"].(map[string]any); ok {
				out = append(out, Turn{
					Role: "tool_call",
					Name: stringOf(call["name"]),
					Text: jsonText(call["args"]),
				})
			}
			if resp, ok := part["functionResponse"].(map[string]any); ok {
				out = append(out, Turn{
					Role: "tool_result",
					Name: stringOf(resp["name"]),
					Text: jsonText(resp["response"]),
				})
			}
		}
	}
	return out
}

// ── Fallback ───────────────────────────────────────────────────────────────

// genericTurns scrapes an unrecognised body for prompt-shaped fields so a new
// or niche endpoint still archives something rather than nothing.
func genericTurns(root map[string]any) []Turn {
	if turns := chatTurns(root); len(turns) > 0 {
		return turns
	}
	if turns := responsesTurns(root); len(turns) > 0 {
		return turns
	}
	keys := make([]string, 0, len(root))
	for k := range root {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	var out []Turn
	for _, k := range keys {
		switch strings.ToLower(k) {
		case "prompt", "query", "input", "text", "question", "description":
			if text := joinTexts(contentStrings(root[k])); text != "" {
				out = append(out, Turn{Role: "user", Text: text})
			}
		}
	}
	return out
}

// ── helpers ────────────────────────────────────────────────────────────────

func arrayOf(value any) []any {
	if list, ok := value.([]any); ok {
		return list
	}
	return nil
}

func stringOf(value any) string {
	if s, ok := value.(string); ok {
		return strings.TrimSpace(s)
	}
	return ""
}

func firstNonEmpty(values ...string) string {
	for _, v := range values {
		if v != "" {
			return v
		}
	}
	return ""
}

func joinTexts(values []string) string {
	out := make([]string, 0, len(values))
	for _, v := range values {
		if v = strings.TrimSpace(v); v != "" {
			out = append(out, v)
		}
	}
	return strings.Join(out, "\n")
}

// contentStrings pulls readable text out of every content shape these APIs
// use: a bare string, a list of typed parts, or a single part object.
func contentStrings(value any) []string {
	switch typed := value.(type) {
	case string:
		return []string{typed}
	case []any:
		out := make([]string, 0, len(typed))
		for _, raw := range typed {
			switch part := raw.(type) {
			case string:
				out = append(out, part)
			case map[string]any:
				if text := stringOf(part["text"]); text != "" {
					out = append(out, text)
					continue
				}
				// Anthropic tool_result content, and Responses output blobs.
				if nested := contentStrings(part["content"]); len(nested) > 0 {
					out = append(out, nested...)
				}
			}
		}
		return out
	case map[string]any:
		if text := stringOf(typed["text"]); text != "" {
			return []string{text}
		}
		if nested := contentStrings(typed["content"]); len(nested) > 0 {
			return nested
		}
	}
	return nil
}

// jsonText renders a structured value (tool arguments, tool output) compactly.
func jsonText(value any) string {
	if value == nil {
		return ""
	}
	if s, ok := value.(string); ok {
		return strings.TrimSpace(s)
	}
	encoded, err := json.Marshal(value)
	if err != nil {
		return fmt.Sprintf("%v", value)
	}
	return string(encoded)
}
