// Package capture ships a copy of every audited gateway request to an external
// archive (sub2api-sidecar) through a Redis queue.
//
// It is deliberately independent of the Prompt Audit feature in
// internal/securityaudit: capture stays on when risk control is disabled, does
// not need a Guard endpoint, never blocks the gateway hot path and never
// influences the outcome of a request.
//
// It also no longer borrows securityaudit's prompt extractor. That one serves a
// content scanner and, for an archive, loses the three things that matter most:
// it truncates at 64 KiB, drops every tool call and tool result, and reorders
// turns. See transcript.go.
package capture

import (
	"time"

	"github.com/Wei-Shaw/sub2api/internal/securityaudit"
)

// payloadVersion is bumped whenever the wire shape changes in a way the
// consumer cannot ignore. The sidecar rejects versions it does not know.
//
// v2 carries a full chronological conversation including tool activity, plus a
// per-turn hash chain, replacing v1's truncated text-only prompt.
const payloadVersion = 2

// Payload is one archived request. Field names are the contract with the
// sidecar; keep them stable.
type Payload struct {
	Version      int          `json:"v"`
	CapturedAt   time.Time    `json:"captured_at"`
	RequestID    string       `json:"request_id"`
	Stage        string       `json:"stage"`
	User         User         `json:"user"`
	APIKey       APIKey       `json:"api_key"`
	Group        Group        `json:"group"`
	Route        Route        `json:"route"`
	Conversation Conversation `json:"conversation"`
}

type User struct {
	ID       int64  `json:"id"`
	Username string `json:"username"`
	Email    string `json:"email"`
}

type APIKey struct {
	ID   int64  `json:"id"`
	Name string `json:"name"`
}

type Group struct {
	ID   *int64 `json:"id"`
	Name string `json:"name"`
}

type Route struct {
	Provider string `json:"provider"`
	Endpoint string `json:"endpoint"`
	Protocol string `json:"protocol"`
	Model    string `json:"model"`
}

// Conversation is the complete exchange this request replayed.
//
// These APIs are stateless: every request carries the whole conversation so
// far. One of these is therefore a full snapshot, and the last request of a
// conversation reconstructs all of it.
type Conversation struct {
	// Text is the rendered conversation in chronological order, tool calls and
	// tool results included.
	Text string `json:"text"`
	Hash string `json:"hash"`
	// Chain is one short hash per turn, in order. The archive uses it to
	// recognise that this request continues a conversation it already holds:
	// the stored chain is a prefix of this one. That test is exact, so
	// unrelated conversations are never merged.
	Chain     []string `json:"chain"`
	Chars     int      `json:"chars"`
	Turns     int      `json:"turns"`
	ToolCalls int      `json:"tool_calls"`
	Truncated bool     `json:"truncated"`
}

// BuildPayload normalises one audited gateway request into an archive record.
// Requests carrying nothing archivable return ErrNoTranscript and are skipped.
func BuildPayload(req securityaudit.Request, now time.Time) (*Payload, error) {
	transcript, err := ExtractTranscript(req.Protocol, req.Body)
	if err != nil {
		return nil, err
	}

	toolCalls := 0
	for _, t := range transcript.Turns {
		if t.Role == "tool_call" {
			toolCalls++
		}
	}

	stage := req.Stage
	if stage == "" {
		stage = "http"
	}
	groupID := req.GroupID
	if groupID != nil {
		id := *groupID
		groupID = &id
	}

	return &Payload{
		Version:    payloadVersion,
		CapturedAt: now.UTC(),
		RequestID:  req.RequestID,
		Stage:      stage,
		User: User{
			ID:       req.UserID,
			Username: req.Username,
			Email:    req.UserEmail,
		},
		APIKey: APIKey{ID: req.APIKeyID, Name: req.APIKeyName},
		Group:  Group{ID: groupID, Name: req.GroupName},
		Route: Route{
			Provider: req.Provider,
			Endpoint: req.Endpoint,
			Protocol: req.Protocol,
			Model:    req.Model,
		},
		Conversation: Conversation{
			Text:      transcript.Text,
			Hash:      transcript.Hash,
			Chain:     transcript.Chain,
			Chars:     transcript.Chars,
			Turns:     len(transcript.Turns),
			ToolCalls: toolCalls,
			Truncated: transcript.Truncated,
		},
	}, nil
}
