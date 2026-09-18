// Package capture ships a copy of every audited gateway request to an external
// archive (sub2api-sidecar) through a Redis queue.
//
// It is deliberately independent of the Prompt Audit feature in
// internal/securityaudit: capture stays on when risk control is disabled, does
// not need a Guard endpoint, never blocks the gateway hot path and never
// influences the outcome of a request. The only thing the two share is the
// normalised securityaudit.Request that the gateway already builds, and the
// multi-protocol prompt extractor that turns a raw body into transcript text.
package capture

import (
	"time"

	"github.com/Wei-Shaw/sub2api/internal/securityaudit"
)

// payloadVersion is bumped whenever the wire shape changes in a way the
// consumer cannot ignore. The sidecar rejects versions it does not know.
const payloadVersion = 1

// Payload is one archived request. Field names are the contract with the
// sidecar; keep them stable.
type Payload struct {
	Version    int       `json:"v"`
	CapturedAt time.Time `json:"captured_at"`
	RequestID  string    `json:"request_id"`
	Stage      string    `json:"stage"`
	User       User      `json:"user"`
	APIKey     APIKey    `json:"api_key"`
	Group      Group     `json:"group"`
	Route      Route     `json:"route"`
	Prompt     Prompt    `json:"prompt"`
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

// Prompt carries the unredacted transcript. Text is capped by the extractor
// (securityaudit.DefaultFullPromptMaxRunes), so one payload is bounded even
// when a client sends a very long conversation.
type Prompt struct {
	Text     string `json:"text"`
	Hash     string `json:"hash"`
	Chars    int    `json:"chars"`
	Messages int    `json:"messages"`
}

// BuildPayload normalises one audited gateway request into an archive record.
//
// The multi-protocol extraction is reused from securityaudit rather than
// reimplemented: capture must never have to know how OpenAI Chat, Responses,
// Anthropic Messages, Gemini or the WebSocket bridge shape a request body.
// Requests carrying no user text return securityaudit.ErrNoPromptText and are
// skipped by the caller — there is nothing to archive.
func BuildPayload(req securityaudit.Request, now time.Time) (*Payload, error) {
	snapshot, err := securityaudit.ExtractPromptSnapshot(req)
	if err != nil {
		return nil, err
	}
	stage := snapshot.Stage
	if stage == "" {
		stage = "http"
	}
	return &Payload{
		Version:    payloadVersion,
		CapturedAt: now.UTC(),
		RequestID:  snapshot.RequestID,
		Stage:      stage,
		User: User{
			ID:       snapshot.UserID,
			Username: snapshot.UsernameSnapshot,
			Email:    snapshot.UserEmailSnapshot,
		},
		APIKey: APIKey{ID: snapshot.APIKeyID, Name: snapshot.APIKeyNameSnapshot},
		Group:  Group{ID: snapshot.GroupID, Name: snapshot.GroupName},
		Route: Route{
			Provider: snapshot.Provider,
			Endpoint: snapshot.Endpoint,
			Protocol: snapshot.Protocol,
			Model:    snapshot.Model,
		},
		Prompt: Prompt{
			Text:     snapshot.FullPrompt,
			Hash:     snapshot.PromptHash,
			Chars:    snapshot.PromptLength,
			Messages: snapshot.MessageCount,
		},
	}, nil
}
