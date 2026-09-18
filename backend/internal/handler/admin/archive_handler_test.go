package admin

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/gin-gonic/gin"
)

// fakeSidecar stands in for sub2api-sidecar and records what reached it.
type fakeSidecar struct {
	server    *httptest.Server
	lastPath  string
	lastQuery string
	lastAuth  string
	status    int
	body      string
}

func newFakeSidecar(t *testing.T) *fakeSidecar {
	t.Helper()
	f := &fakeSidecar{status: http.StatusOK, body: `{"items":[]}`}
	f.server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		f.lastPath = r.URL.Path
		f.lastQuery = r.URL.RawQuery
		f.lastAuth = r.Header.Get("Authorization")
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(f.status)
		_, _ = io.WriteString(w, f.body)
	}))
	t.Cleanup(f.server.Close)
	return f
}

func newArchiveRouter(t *testing.T, cfg config.ArchiveConfig) *gin.Engine {
	t.Helper()
	gin.SetMode(gin.TestMode)
	h := NewArchiveHandler(&config.Config{Archive: cfg})
	r := gin.New()
	g := r.Group("/api/v1/admin/archive")
	g.GET("/status", h.Status)
	g.GET("/conversations", h.Conversations)
	g.GET("/conversations/:id", h.Conversation)
	g.GET("/conversations/:id/requests", h.ConversationRequests)
	g.GET("/users", h.Users)
	g.GET("/stats", h.Stats)
	return r
}

func do(t *testing.T, r *gin.Engine, path string) *httptest.ResponseRecorder {
	t.Helper()
	w := httptest.NewRecorder()
	r.ServeHTTP(w, httptest.NewRequest(http.MethodGet, path, nil))
	return w
}

// The sidecar token authorises the gateway, not the browser. It must be added
// server-side and must never appear in a response.
func TestProxyAttachesSidecarTokenServerSide(t *testing.T) {
	sidecar := newFakeSidecar(t)
	r := newArchiveRouter(t, config.ArchiveConfig{
		SidecarURL: sidecar.server.URL, SidecarToken: "sidecar-secret", TimeoutMS: 5000,
	})

	w := do(t, r, "/api/v1/admin/archive/conversations?user_id=42&q=hello")
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", w.Code, w.Body.String())
	}
	if sidecar.lastAuth != "Bearer sidecar-secret" {
		t.Errorf("upstream Authorization = %q", sidecar.lastAuth)
	}
	if sidecar.lastPath != "/api/archive/conversations" {
		t.Errorf("upstream path = %q", sidecar.lastPath)
	}
	// Filters must survive the hop, or the console silently returns everything.
	if !strings.Contains(sidecar.lastQuery, "user_id=42") || !strings.Contains(sidecar.lastQuery, "q=hello") {
		t.Errorf("query not forwarded: %q", sidecar.lastQuery)
	}
	if strings.Contains(w.Body.String(), "sidecar-secret") {
		t.Error("the sidecar token leaked into the response")
	}
}

func TestProxyForwardsPathParameters(t *testing.T) {
	sidecar := newFakeSidecar(t)
	r := newArchiveRouter(t, config.ArchiveConfig{
		SidecarURL: sidecar.server.URL, SidecarToken: "t", TimeoutMS: 5000,
	})

	do(t, r, "/api/v1/admin/archive/conversations/17")
	if sidecar.lastPath != "/api/archive/conversations/17" {
		t.Errorf("path = %q", sidecar.lastPath)
	}
	do(t, r, "/api/v1/admin/archive/conversations/17/requests")
	if sidecar.lastPath != "/api/archive/conversations/17/requests" {
		t.Errorf("path = %q", sidecar.lastPath)
	}
}

// An id from the URL is attacker-controlled; it must not be able to reach a
// different sidecar endpoint such as the job trigger.
func TestProxyEscapesPathParameter(t *testing.T) {
	sidecar := newFakeSidecar(t)
	r := newArchiveRouter(t, config.ArchiveConfig{
		SidecarURL: sidecar.server.URL, SidecarToken: "t", TimeoutMS: 5000,
	})

	do(t, r, "/api/v1/admin/archive/conversations/..%2F..%2Fjobs%2Fquota-report%2Frun")
	if strings.Contains(sidecar.lastPath, "/jobs/") {
		t.Errorf("path traversal reached %q", sidecar.lastPath)
	}
}

// Not configured and unreachable are different problems; the console must be
// able to tell them apart.
func TestNotConfiguredReturns503(t *testing.T) {
	r := newArchiveRouter(t, config.ArchiveConfig{})

	w := do(t, r, "/api/v1/admin/archive/conversations")
	if w.Code != http.StatusServiceUnavailable {
		t.Errorf("status = %d, want 503", w.Code)
	}
	var body map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if body["code"] != "archive_not_configured" {
		t.Errorf("code = %v", body["code"])
	}

	w = do(t, r, "/api/v1/admin/archive/status")
	if w.Code != http.StatusOK {
		t.Fatalf("status endpoint = %d, want 200 even when unconfigured", w.Code)
	}
	if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if body["enabled"] != false {
		t.Errorf("enabled = %v, want false", body["enabled"])
	}
}

func TestUnreachableSidecarReturns502(t *testing.T) {
	r := newArchiveRouter(t, config.ArchiveConfig{
		// A port nothing listens on.
		SidecarURL: "http://127.0.0.1:1", SidecarToken: "t", TimeoutMS: 1000,
	})

	w := do(t, r, "/api/v1/admin/archive/conversations")
	if w.Code != http.StatusBadGateway {
		t.Errorf("status = %d, want 502", w.Code)
	}
	var body map[string]any
	_ = json.Unmarshal(w.Body.Bytes(), &body)
	if body["code"] != "archive_unreachable" {
		t.Errorf("code = %v", body["code"])
	}
	// The upstream address is infrastructure detail; it should not be handed to
	// the browser along with the error.
	if strings.Contains(w.Body.String(), "127.0.0.1:1") {
		t.Error("the response leaked the upstream address")
	}
}

// A 404 from the sidecar must stay a 404, or the console cannot distinguish a
// missing conversation from a broken archive.
func TestUpstreamStatusIsPreserved(t *testing.T) {
	sidecar := newFakeSidecar(t)
	sidecar.status = http.StatusNotFound
	sidecar.body = `{"error":"not found"}`
	r := newArchiveRouter(t, config.ArchiveConfig{
		SidecarURL: sidecar.server.URL, SidecarToken: "t", TimeoutMS: 5000,
	})

	w := do(t, r, "/api/v1/admin/archive/conversations/999")
	if w.Code != http.StatusNotFound {
		t.Errorf("status = %d, want the upstream 404", w.Code)
	}
}

func TestEnabledRequiresBothURLAndToken(t *testing.T) {
	cases := []struct {
		name string
		cfg  config.ArchiveConfig
		want bool
	}{
		{"both set", config.ArchiveConfig{SidecarURL: "http://x", SidecarToken: "t"}, true},
		{"url only", config.ArchiveConfig{SidecarURL: "http://x"}, false},
		{"token only", config.ArchiveConfig{SidecarToken: "t"}, false},
		{"neither", config.ArchiveConfig{}, false},
		{"whitespace only", config.ArchiveConfig{SidecarURL: "  ", SidecarToken: "  "}, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := tc.cfg.Enabled(); got != tc.want {
				t.Errorf("Enabled() = %t, want %t", got, tc.want)
			}
		})
	}
}

// The console decides whether to render the archive from status.enabled. The
// sidecar's health payload has no top-level "enabled" — only a nested one
// meaning "ingest is on" — so passing it through verbatim made a working
// archive read as "not configured". The contract belongs to this handler.
func TestStatusAlwaysReportsEnabledWhenConfigured(t *testing.T) {
	sidecar := newFakeSidecar(t)
	sidecar.body = `{"archive":{"store":{"conversations":262,"requests":3146}},"jobs":["quota-report"]}`
	r := newArchiveRouter(t, config.ArchiveConfig{
		SidecarURL: sidecar.server.URL, SidecarToken: "t", TimeoutMS: 5000,
	})

	w := do(t, r, "/api/v1/admin/archive/status")
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d", w.Code)
	}
	var body map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if body["enabled"] != true {
		t.Errorf("enabled = %v, want true: the console hides the page without it", body["enabled"])
	}
	if body["reachable"] != true {
		t.Errorf("reachable = %v, want true", body["reachable"])
	}
	// The archive totals still have to reach the page header.
	archive, ok := body["archive"].(map[string]any)
	if !ok {
		t.Fatalf("archive block missing: %v", body)
	}
	store, ok := archive["store"].(map[string]any)
	if !ok || store["conversations"] != float64(262) {
		t.Errorf("store totals lost: %v", archive)
	}
}

// A health payload this build does not understand must not flip the page to
// "not configured"; the archive is still there.
func TestStatusSurvivesUnexpectedHealthPayload(t *testing.T) {
	sidecar := newFakeSidecar(t)
	sidecar.body = `{"something":"else"}`
	r := newArchiveRouter(t, config.ArchiveConfig{
		SidecarURL: sidecar.server.URL, SidecarToken: "t", TimeoutMS: 5000,
	})

	w := do(t, r, "/api/v1/admin/archive/status")
	var body map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if body["enabled"] != true || body["reachable"] != true {
		t.Errorf("status = %v, want enabled and reachable", body)
	}
}
