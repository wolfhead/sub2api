package capture

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"sync"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/Wei-Shaw/sub2api/internal/securityaudit"
)

// fakeSink records what the service delivers and can be told to fail or block.
type fakeSink struct {
	mu       sync.Mutex
	payloads [][]byte
	err      error
	block    chan struct{}
	depth    int64
	depthErr error
}

func (f *fakeSink) Name() string { return "fake" }

func (f *fakeSink) Send(ctx context.Context, payload []byte) error {
	if f.block != nil {
		select {
		case <-f.block:
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.err != nil {
		return f.err
	}
	f.payloads = append(f.payloads, append([]byte(nil), payload...))
	return nil
}

func (f *fakeSink) Depth(context.Context) (int64, error) { return f.depth, f.depthErr }

func (f *fakeSink) count() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.payloads)
}

func (f *fakeSink) last() []byte {
	f.mu.Lock()
	defer f.mu.Unlock()
	if len(f.payloads) == 0 {
		return nil
	}
	return f.payloads[len(f.payloads)-1]
}

func quietLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, &slog.HandlerOptions{Level: slog.LevelError}))
}

func enabledConfig() config.CaptureConfig {
	return config.CaptureConfig{
		Enabled:                 true,
		RedisKey:                "test:capture",
		MaxQueueLength:          100,
		MaxInflight:             8,
		SendTimeoutMS:           1000,
		DepthLogIntervalSeconds: 3600,
	}
}

func chatRequest(requestID string) securityaudit.Request {
	return securityaudit.Request{
		RequestID:  requestID,
		UserID:     42,
		Username:   "alice",
		UserEmail:  "alice@example.com",
		APIKeyID:   7,
		APIKeyName: "workbuddy",
		GroupName:  "default",
		Provider:   "openai",
		Endpoint:   "/v1/chat/completions",
		Protocol:   "openai_chat",
		Model:      "gpt-test",
		Body:       []byte(`{"messages":[{"role":"user","content":"hello archive"}]}`),
		Stage:      "http",
	}
}

// waitFor polls until cond holds or the deadline passes. Submit is asynchronous
// by contract, so tests must not assume delivery happened on return.
func waitFor(t *testing.T, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("condition not met before deadline")
}

func TestSubmitDeliversNormalisedPayload(t *testing.T) {
	sink := &fakeSink{}
	svc := NewService(enabledConfig(), sink, quietLogger())
	defer svc.Shutdown(context.Background())

	svc.Submit(chatRequest("req-1"))
	waitFor(t, func() bool { return sink.count() == 1 })

	var payload Payload
	if err := json.Unmarshal(sink.last(), &payload); err != nil {
		t.Fatalf("unmarshal payload: %v", err)
	}
	if payload.Version != payloadVersion {
		t.Errorf("version = %d, want %d", payload.Version, payloadVersion)
	}
	if payload.RequestID != "req-1" {
		t.Errorf("request_id = %q, want req-1", payload.RequestID)
	}
	if payload.User.ID != 42 || payload.User.Email != "alice@example.com" {
		t.Errorf("user not carried through: %+v", payload.User)
	}
	if payload.APIKey.ID != 7 || payload.APIKey.Name != "workbuddy" {
		t.Errorf("api key not carried through: %+v", payload.APIKey)
	}
	if payload.Route.Protocol != "openai_chat" || payload.Route.Model != "gpt-test" {
		t.Errorf("route not carried through: %+v", payload.Route)
	}
	if payload.Conversation.Text == "" || payload.Conversation.Hash == "" {
		t.Errorf("conversation not extracted: %+v", payload.Conversation)
	}
	if got := svc.Stats().Submitted; got != 1 {
		t.Errorf("submitted = %d, want 1", got)
	}
}

func TestDisabledServiceNeverDelivers(t *testing.T) {
	cfg := enabledConfig()
	cfg.Enabled = false
	sink := &fakeSink{}
	svc := NewService(cfg, sink, quietLogger())

	if svc.Enabled() {
		t.Fatal("service reports enabled with Enabled=false")
	}
	svc.Submit(chatRequest("req-off"))
	time.Sleep(50 * time.Millisecond)
	if sink.count() != 0 {
		t.Errorf("disabled service delivered %d payloads", sink.count())
	}
}

func TestNilServiceIsInert(t *testing.T) {
	var svc *Service
	if svc.Enabled() {
		t.Fatal("nil service reports enabled")
	}
	// Must not panic: handlers hold this as a plain field and call it directly.
	svc.Submit(chatRequest("req-nil"))
	svc.Shutdown(context.Background())
}

func TestSubmitDropsWhenInflightBudgetExhausted(t *testing.T) {
	cfg := enabledConfig()
	cfg.MaxInflight = 1
	sink := &fakeSink{block: make(chan struct{})}
	svc := NewService(cfg, sink, quietLogger())

	// First submit occupies the only slot and parks inside Send.
	svc.Submit(chatRequest("req-blocking"))
	waitFor(t, func() bool { return len(svc.slots) == 1 })

	for i := 0; i < 5; i++ {
		svc.Submit(chatRequest("req-dropped"))
	}
	if got := svc.Stats().Dropped; got != 5 {
		t.Errorf("dropped = %d, want 5", got)
	}

	close(sink.block)
	svc.Shutdown(context.Background())
	if got := svc.Stats().Submitted; got != 1 {
		t.Errorf("submitted = %d, want 1", got)
	}
}

func TestSinkErrorIsCountedNotPanicked(t *testing.T) {
	sink := &fakeSink{err: errors.New("redis down")}
	svc := NewService(enabledConfig(), sink, quietLogger())
	defer svc.Shutdown(context.Background())

	svc.Submit(chatRequest("req-fail"))
	waitFor(t, func() bool { return svc.Stats().Failed == 1 })
	if got := svc.Stats().Submitted; got != 0 {
		t.Errorf("submitted = %d, want 0", got)
	}
}

func TestRequestWithoutPromptTextIsSkipped(t *testing.T) {
	sink := &fakeSink{}
	svc := NewService(enabledConfig(), sink, quietLogger())
	defer svc.Shutdown(context.Background())

	req := chatRequest("req-empty")
	req.Body = []byte(`{"input":"","messages":[]}`)
	svc.Submit(req)

	waitFor(t, func() bool { return svc.Stats().Skipped == 1 })
	if sink.count() != 0 {
		t.Errorf("delivered %d payloads for a body with no prompt text", sink.count())
	}
	if got := svc.Stats().Failed; got != 0 {
		t.Errorf("failed = %d; an empty prompt is expected, not an error", got)
	}
}

// The handler hands over a body it may reuse once the request returns. Submit
// must copy before the delivery goroutine reads it.
func TestSubmitCopiesRequestBody(t *testing.T) {
	sink := &fakeSink{block: make(chan struct{})}
	svc := NewService(enabledConfig(), sink, quietLogger())

	req := chatRequest("req-mutate")
	svc.Submit(req)
	waitFor(t, func() bool { return len(svc.slots) == 1 })

	for i := range req.Body {
		req.Body[i] = 'x'
	}
	close(sink.block)
	waitFor(t, func() bool { return sink.count() == 1 })
	svc.Shutdown(context.Background())

	var payload Payload
	if err := json.Unmarshal(sink.last(), &payload); err != nil {
		t.Fatalf("unmarshal payload: %v", err)
	}
	if payload.Conversation.Text == "" {
		t.Fatal("prompt text empty: the body was read after the caller overwrote it")
	}
}

func TestBuildPayloadCarriesGroupAndStage(t *testing.T) {
	groupID := int64(9)
	req := chatRequest("req-group")
	req.GroupID = &groupID
	req.GroupName = "vip"
	req.Stage = "first_turn"

	payload, err := BuildPayload(req, time.Unix(1700000000, 0))
	if err != nil {
		t.Fatalf("BuildPayload: %v", err)
	}
	if payload.Group.ID == nil || *payload.Group.ID != 9 || payload.Group.Name != "vip" {
		t.Errorf("group not carried through: %+v", payload.Group)
	}
	if payload.Stage != "first_turn" {
		t.Errorf("stage = %q, want first_turn", payload.Stage)
	}
	if !payload.CapturedAt.Equal(time.Unix(1700000000, 0).UTC()) {
		t.Errorf("captured_at = %v, want the supplied instant in UTC", payload.CapturedAt)
	}
}
