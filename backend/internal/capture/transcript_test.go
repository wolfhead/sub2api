package capture

import (
	"errors"
	"strings"
	"testing"
)

// The three defects this extractor exists to fix, stated as tests.
//
// Reusing securityaudit's extractor cost the archive: tool activity (dropped
// entirely), chronological order (turns were reordered to put the most
// suspicious content first) and completeness (truncated at 64 KiB). Each is
// pinned below.

func TestResponsesKeepsToolActivity(t *testing.T) {
	body := []byte(`{
      "instructions": "you are a coding agent",
      "input": [
        {"role":"user","content":[{"type":"input_text","text":"list the repo"}]},
        {"type":"function_call","call_id":"c1","name":"shell","arguments":"{\"cmd\":\"ls -la\"}"},
        {"type":"function_call_output","call_id":"c1","output":"README.md\ngo.mod"},
        {"role":"assistant","content":[{"type":"output_text","text":"two files"}]}
      ]}`)

	tr, err := ExtractTranscript("openai_responses", body)
	if err != nil {
		t.Fatalf("ExtractTranscript: %v", err)
	}
	if len(tr.Turns) != 5 {
		t.Fatalf("got %d turns, want 5: %+v", len(tr.Turns), tr.Turns)
	}

	want := []struct{ role, name, contains string }{
		{"system", "", "coding agent"},
		{"user", "", "list the repo"},
		{"tool_call", "shell", "ls -la"},
		{"tool_result", "c1", "go.mod"},
		{"assistant", "", "two files"},
	}
	for i, w := range want {
		got := tr.Turns[i]
		if got.Role != w.role {
			t.Errorf("turn %d role = %q, want %q", i, got.Role, w.role)
		}
		if got.Name != w.name {
			t.Errorf("turn %d name = %q, want %q", i, got.Name, w.name)
		}
		if !strings.Contains(got.Text, w.contains) {
			t.Errorf("turn %d text = %q, want it to contain %q", i, got.Text, w.contains)
		}
	}
}

// Order is the archive's whole value: a reviewer reads it as a timeline.
func TestTranscriptIsChronological(t *testing.T) {
	body := []byte(`{"input":[
        {"role":"user","content":[{"type":"input_text","text":"FIRST"}]},
        {"role":"assistant","content":[{"type":"output_text","text":"SECOND"}]},
        {"role":"user","content":[{"type":"input_text","text":"THIRD"}]}
      ]}`)

	tr, err := ExtractTranscript("openai_responses", body)
	if err != nil {
		t.Fatalf("ExtractTranscript: %v", err)
	}
	first := strings.Index(tr.Text, "FIRST")
	second := strings.Index(tr.Text, "SECOND")
	third := strings.Index(tr.Text, "THIRD")
	if !(first < second && second < third) {
		t.Errorf("turns are not in send order: FIRST@%d SECOND@%d THIRD@%d\n%s",
			first, second, third, tr.Text)
	}
}

func TestAnthropicKeepsToolUseAndResult(t *testing.T) {
	body := []byte(`{
      "system":"be careful",
      "messages":[
        {"role":"user","content":[{"type":"text","text":"delete the temp files"}]},
        {"role":"assistant","content":[
           {"type":"thinking","thinking":"I should check first"},
           {"type":"text","text":"checking"},
           {"type":"tool_use","id":"t1","name":"bash","input":{"command":"rm -rf /tmp/x"}}
        ]},
        {"role":"user","content":[
           {"type":"tool_result","tool_use_id":"t1","content":[{"type":"text","text":"removed 3 files"}]}
        ]}
      ]}`)

	tr, err := ExtractTranscript("anthropic_messages", body)
	if err != nil {
		t.Fatalf("ExtractTranscript: %v", err)
	}
	joined := tr.Text
	for _, want := range []string{"be careful", "delete the temp files", "I should check first",
		"checking", "rm -rf /tmp/x", "removed 3 files"} {
		if !strings.Contains(joined, want) {
			t.Errorf("transcript lost %q:\n%s", want, joined)
		}
	}
	var toolCalls, toolResults int
	for _, turn := range tr.Turns {
		switch turn.Role {
		case "tool_call":
			toolCalls++
			if turn.Name != "bash" {
				t.Errorf("tool name = %q, want bash", turn.Name)
			}
		case "tool_result":
			toolResults++
		}
	}
	if toolCalls != 1 || toolResults != 1 {
		t.Errorf("tool_call=%d tool_result=%d, want 1/1", toolCalls, toolResults)
	}
}

func TestChatCompletionsKeepsToolCalls(t *testing.T) {
	body := []byte(`{"messages":[
        {"role":"system","content":"sys"},
        {"role":"user","content":"weather?"},
        {"role":"assistant","reasoning_content":"need a tool","tool_calls":[
           {"id":"call_1","type":"function","function":{"name":"get_weather","arguments":"{\"city\":\"SH\"}"}}
        ]},
        {"role":"tool","tool_call_id":"call_1","content":"22C sunny"},
        {"role":"assistant","content":"It is 22C."}
      ]}`)

	tr, err := ExtractTranscript("openai_chat_completions", body)
	if err != nil {
		t.Fatalf("ExtractTranscript: %v", err)
	}
	for _, want := range []string{"sys", "weather?", "need a tool", "get_weather", "SH", "22C sunny", "It is 22C."} {
		if !strings.Contains(tr.Text, want) {
			t.Errorf("transcript lost %q:\n%s", want, tr.Text)
		}
	}
}

func TestGeminiKeepsFunctionParts(t *testing.T) {
	body := []byte(`{
      "systemInstruction":{"parts":[{"text":"sys prompt"}]},
      "contents":[
        {"role":"user","parts":[{"text":"run it"}]},
        {"role":"model","parts":[{"functionCall":{"name":"exec","args":{"cmd":"go test"}}}]},
        {"role":"user","parts":[{"functionResponse":{"name":"exec","response":{"out":"ok"}}}]}
      ]}`)

	tr, err := ExtractTranscript("gemini", body)
	if err != nil {
		t.Fatalf("ExtractTranscript: %v", err)
	}
	for _, want := range []string{"sys prompt", "run it", "exec", "go test", "ok"} {
		if !strings.Contains(tr.Text, want) {
			t.Errorf("transcript lost %q:\n%s", want, tr.Text)
		}
	}
}

// A conversation of 120k characters is ordinary here. Keeping only the first
// 64 KiB, as the scanner-oriented extractor did, threw away roughly half of
// production traffic — and the half that was thrown away was the newest.
func TestLongConversationIsNotTruncatedAtTheOldLimit(t *testing.T) {
	var b strings.Builder
	b.WriteString(`{"input":[`)
	for i := 0; i < 400; i++ {
		if i > 0 {
			b.WriteString(",")
		}
		b.WriteString(`{"role":"user","content":[{"type":"input_text","text":"`)
		b.WriteString(strings.Repeat("x", 500))
		b.WriteString(`"}]}`)
	}
	// The marker sits at the very end, exactly where truncation used to bite.
	b.WriteString(`,{"role":"user","content":[{"type":"input_text","text":"FINAL_MARKER"}]}]}`)

	tr, err := ExtractTranscript("openai_responses", []byte(b.String()))
	if err != nil {
		t.Fatalf("ExtractTranscript: %v", err)
	}
	if tr.Chars <= 65536 {
		t.Fatalf("test conversation is only %d runes; it must exceed the old 64 KiB limit", tr.Chars)
	}
	if tr.Truncated {
		t.Errorf("a %d-rune conversation was truncated", tr.Chars)
	}
	if !strings.Contains(tr.Text, "FINAL_MARKER") {
		t.Error("the newest turn was cut off, which is exactly the old defect")
	}
}

func TestTranscriptTruncatesOnlyAtTheDefensiveLimit(t *testing.T) {
	huge := strings.Repeat("y", MaxTranscriptRunes+1000)
	body := []byte(`{"input":[{"role":"user","content":[{"type":"input_text","text":"` + huge + `"}]}]}`)

	tr, err := ExtractTranscript("openai_responses", body)
	if err != nil {
		t.Fatalf("ExtractTranscript: %v", err)
	}
	if !tr.Truncated {
		t.Error("a payload past the defensive limit was not marked truncated")
	}
	if !strings.Contains(tr.Text, "truncated by the archive") {
		t.Error("truncation is not visible in the stored text")
	}
}

// The chain is what lets the archive recognise a continued conversation. A
// request that appends a turn must extend the previous chain, not replace it.
func TestChainGrowsAsAPrefix(t *testing.T) {
	round1 := []byte(`{"input":[
        {"role":"user","content":[{"type":"input_text","text":"step one"}]}
      ]}`)
	round2 := []byte(`{"input":[
        {"role":"user","content":[{"type":"input_text","text":"step one"}]},
        {"type":"function_call","call_id":"c1","name":"shell","arguments":"{\"cmd\":\"ls\"}"},
        {"type":"function_call_output","call_id":"c1","output":"a.txt"}
      ]}`)

	t1, err := ExtractTranscript("openai_responses", round1)
	if err != nil {
		t.Fatalf("round 1: %v", err)
	}
	t2, err := ExtractTranscript("openai_responses", round2)
	if err != nil {
		t.Fatalf("round 2: %v", err)
	}

	if len(t2.Chain) != len(t1.Chain)+2 {
		t.Fatalf("chain lengths %d -> %d, want +2", len(t1.Chain), len(t2.Chain))
	}
	for i, h := range t1.Chain {
		if t2.Chain[i] != h {
			t.Fatalf("chain diverged at %d: %q vs %q", i, t2.Chain[i], h)
		}
	}
	// And the tool round must change the overall hash, or the archive cannot
	// tell two agent steps apart — the symptom that started this work.
	if t1.Hash == t2.Hash {
		t.Error("a tool round produced an identical conversation hash")
	}
}

// Two consecutive agent steps differ only by tool activity. Under the old
// extractor they hashed identically, which is why 64% of the archive looked
// like duplicates.
func TestToolOnlyRoundChangesTheHash(t *testing.T) {
	base := `{"input":[{"role":"user","content":[{"type":"input_text","text":"do the thing"}]}`
	stepA := []byte(base + `]}`)
	stepB := []byte(base + `,{"type":"function_call","call_id":"c1","name":"shell","arguments":"{\"cmd\":\"ls\"}"}]}`)

	a, err := ExtractTranscript("openai_responses", stepA)
	if err != nil {
		t.Fatalf("step A: %v", err)
	}
	b, err := ExtractTranscript("openai_responses", stepB)
	if err != nil {
		t.Fatalf("step B: %v", err)
	}
	if a.Hash == b.Hash {
		t.Error("two agent steps hashed the same; tool activity is still invisible")
	}
}

func TestWebSocketFrameUnwraps(t *testing.T) {
	body := []byte(`{"type":"response.create","response":{"input":[
        {"role":"user","content":[{"type":"input_text","text":"ws turn"}]}
      ]}}`)
	tr, err := ExtractTranscript("responses_websocket", body)
	if err != nil {
		t.Fatalf("ExtractTranscript: %v", err)
	}
	if !strings.Contains(tr.Text, "ws turn") {
		t.Errorf("websocket frame not unwrapped: %q", tr.Text)
	}
}

func TestNonCreateWebSocketFrameIsSkipped(t *testing.T) {
	body := []byte(`{"type":"response.cancel","response":{}}`)
	if _, err := ExtractTranscript("responses_websocket", body); !errors.Is(err, ErrNoTranscript) {
		t.Errorf("err = %v, want ErrNoTranscript", err)
	}
}

func TestEmptyAndUnparseableBodies(t *testing.T) {
	cases := map[string][]byte{
		"invalid json":   []byte(`not json`),
		"empty object":   []byte(`{}`),
		"empty messages": []byte(`{"messages":[]}`),
		"blank text":     []byte(`{"input":[{"role":"user","content":[{"type":"input_text","text":"   "}]}]}`),
	}
	for name, body := range cases {
		t.Run(name, func(t *testing.T) {
			if _, err := ExtractTranscript("openai_responses", body); !errors.Is(err, ErrNoTranscript) {
				t.Errorf("err = %v, want ErrNoTranscript", err)
			}
		})
	}
}

// An unknown endpoint should still archive something rather than silently
// recording nothing.
func TestUnknownProtocolFallsBack(t *testing.T) {
	tr, err := ExtractTranscript("openai_alpha_search", []byte(`{"query":"who owns this repo"}`))
	if err != nil {
		t.Fatalf("ExtractTranscript: %v", err)
	}
	if !strings.Contains(tr.Text, "who owns this repo") {
		t.Errorf("fallback lost the query: %q", tr.Text)
	}
}

func TestToolCallCountIsReported(t *testing.T) {
	body := []byte(`{"input":[
        {"role":"user","content":[{"type":"input_text","text":"go"}]},
        {"type":"function_call","call_id":"c1","name":"a","arguments":"{}"},
        {"type":"function_call","call_id":"c2","name":"b","arguments":"{}"}
      ]}`)
	tr, err := ExtractTranscript("openai_responses", body)
	if err != nil {
		t.Fatalf("ExtractTranscript: %v", err)
	}
	count := 0
	for _, turn := range tr.Turns {
		if turn.Role == "tool_call" {
			count++
		}
	}
	if count != 2 {
		t.Errorf("tool_call turns = %d, want 2", count)
	}
}
