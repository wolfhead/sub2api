package handler

import (
	"context"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/capture"
	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/Wei-Shaw/sub2api/internal/securityaudit"
	middleware2 "github.com/Wei-Shaw/sub2api/internal/server/middleware"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

// countingSink counts deliveries so the tests can assert how many payloads one
// client request produces.
type countingSink struct {
	mu    sync.Mutex
	sent  int
	first []byte
}

func (s *countingSink) Name() string { return "counting" }

func (s *countingSink) Send(_ context.Context, payload []byte) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.sent++
	if s.first == nil {
		s.first = append([]byte(nil), payload...)
	}
	return nil
}

func (s *countingSink) Depth(context.Context) (int64, error) { return 0, nil }

func (s *countingSink) count() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.sent
}

func newCaptureForTest(t *testing.T) (*capture.Service, *countingSink) {
	t.Helper()
	sink := &countingSink{}
	svc := capture.NewService(config.CaptureConfig{
		Enabled:                 true,
		RedisKey:                "test:capture",
		MaxQueueLength:          100,
		MaxInflight:             16,
		SendTimeoutMS:           1000,
		DepthLogIntervalSeconds: 3600,
	}, sink, slog.New(slog.NewTextHandler(io.Discard, &slog.HandlerOptions{Level: slog.LevelError})))
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		svc.Shutdown(ctx)
	})
	return svc, sink
}

func waitForCount(t *testing.T, sink *countingSink, want int) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if sink.count() >= want {
			break
		}
		time.Sleep(5 * time.Millisecond)
	}
	// Give a late duplicate a chance to land before asserting the exact count.
	time.Sleep(50 * time.Millisecond)
	require.Equal(t, want, sink.count())
}

// An HTTP handler may call checkSecurityAudit more than once per request; the
// audit completion cache suppresses the repeat and capture must inherit that,
// otherwise one conversation is archived twice.
func TestCaptureSubmitsOncePerHTTPRequest(t *testing.T) {
	gin.SetMode(gin.TestMode)
	engine := &turnCountingEngine{mode: securityaudit.ModeAsync}
	coordinator := securityaudit.NewCoordinator(nil, engine)
	captureSvc, sink := newCaptureForTest(t)

	recorder := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(recorder)
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/chat/completions", nil)
	subject := middleware2.AuthSubject{UserID: 7, Concurrency: 1}
	body := []byte(`{"messages":[{"role":"user","content":"archive me once"}]}`)

	for i := 0; i < 3; i++ {
		runSecurityAudit(c, nil, coordinator, captureSvc, nil, nil, subject, "openai_chat", "gpt-test", body, "http")
	}

	waitForCount(t, sink, 1)
}

// WebSocket turns share one gin.Context. Each response.create is a separate
// conversation state and must be archived separately.
func TestCaptureSubmitsPerWebSocketTurn(t *testing.T) {
	gin.SetMode(gin.TestMode)
	engine := &turnCountingEngine{mode: securityaudit.ModeAsync}
	coordinator := securityaudit.NewCoordinator(nil, engine)
	captureSvc, sink := newCaptureForTest(t)

	recorder := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(recorder)
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/responses", nil)
	subject := middleware2.AuthSubject{UserID: 7}

	runSecurityAudit(c, nil, coordinator, captureSvc, nil, nil, subject, "openai_responses", "gpt-test",
		[]byte(`{"type":"response.create","response":{"input":"turn one"}}`), "first_turn")
	runSecurityAudit(c, nil, coordinator, captureSvc, nil, nil, subject, "openai_responses", "gpt-test",
		[]byte(`{"type":"response.create","response":{"input":"turn two"}}`), "subsequent_turn")

	waitForCount(t, sink, 2)
}

// A repeated payload inside the same WebSocket turn is de-duplicated by the
// audit path; capture sits behind that check and must not re-archive it.
func TestCaptureSkipsRepeatedPayloadWithinWebSocketTurn(t *testing.T) {
	gin.SetMode(gin.TestMode)
	engine := &turnCountingEngine{mode: securityaudit.ModeBlocking}
	coordinator := securityaudit.NewCoordinator(nil, engine)
	captureSvc, sink := newCaptureForTest(t)

	recorder := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(recorder)
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/responses", nil)
	c.Set(securityAuditWSTurnContextKey, 2)
	payload := []byte(`{"type":"response.create","response":{"input":"same turn"}}`)
	subject := middleware2.AuthSubject{UserID: 7}

	runSecurityAudit(c, nil, coordinator, captureSvc, nil, nil, subject, "openai_responses", "gpt-test", payload, "subsequent_turn")
	runSecurityAudit(c, nil, coordinator, captureSvc, nil, nil, subject, "openai_responses", "gpt-test", payload, "subsequent_turn")

	waitForCount(t, sink, 1)
}

// Archiving is independent of risk control: a nil capture service must leave
// the gateway path working exactly as before.
func TestNilCaptureServiceLeavesAuditUnchanged(t *testing.T) {
	gin.SetMode(gin.TestMode)
	engine := &turnCountingEngine{mode: securityaudit.ModeAsync}
	coordinator := securityaudit.NewCoordinator(nil, engine)

	recorder := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(recorder)
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/chat/completions", nil)

	decision := runSecurityAudit(c, nil, coordinator, nil, nil, nil,
		middleware2.AuthSubject{UserID: 7}, "openai_chat", "gpt-test",
		[]byte(`{"messages":[{"role":"user","content":"hi"}]}`), "http")

	require.NotNil(t, decision)
	require.True(t, decision.AllowNextStage)
	require.Equal(t, int64(1), engine.enqueues.Load())
}
