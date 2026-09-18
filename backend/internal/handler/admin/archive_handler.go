package admin

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/Wei-Shaw/sub2api/internal/pkg/logger"
	middleware2 "github.com/Wei-Shaw/sub2api/internal/server/middleware"
	"github.com/gin-gonic/gin"
	"go.uber.org/zap"
)

// ArchiveHandler exposes the conversation archive owned by sub2api-sidecar to
// the admin console.
//
// The gateway never touches the archive database. It forwards a fixed set of
// read-only endpoints, so reviewing conversations needs nothing more than the
// admin login the operator already has — no SSH tunnel and no second token.
//
// Only the routes listed in proxyRoutes are reachable. A wildcard passthrough
// would also expose the sidecar's job-trigger endpoints, which have no business
// being callable from a browser session.
type ArchiveHandler struct {
	cfg    config.ArchiveConfig
	client *http.Client
}

func NewArchiveHandler(cfg *config.Config) *ArchiveHandler {
	timeout := time.Duration(cfg.Archive.TimeoutMS) * time.Millisecond
	if timeout <= 0 {
		timeout = 20 * time.Second
	}
	return &ArchiveHandler{
		cfg:    cfg.Archive,
		client: &http.Client{Timeout: timeout},
	}
}

func (h *ArchiveHandler) log() *zap.Logger {
	return logger.L().With(zap.String("component", "handler.admin.archive"))
}

// Status lets the console tell "not configured" apart from "configured but
// currently unreachable", which are different problems for whoever is on call.
func (h *ArchiveHandler) Status(c *gin.Context) {
	if !h.cfg.Enabled() {
		c.JSON(http.StatusOK, gin.H{
			"enabled": false,
			"reason":  "archive.sidecar_url / archive.sidecar_token are not configured",
		})
		return
	}
	ctx, cancel := context.WithTimeout(c.Request.Context(), 5*time.Second)
	defer cancel()

	body, status, err := h.fetch(ctx, "/healthz", "")
	if err != nil {
		c.JSON(http.StatusOK, gin.H{"enabled": true, "reachable": false, "error": err.Error()})
		return
	}
	if status != http.StatusOK {
		c.JSON(http.StatusOK, gin.H{"enabled": true, "reachable": false,
			"error": fmt.Sprintf("sidecar returned HTTP %d", status)})
		return
	}
	c.Data(http.StatusOK, "application/json; charset=utf-8", body)
}

// Conversations lists archived conversations.
func (h *ArchiveHandler) Conversations(c *gin.Context) {
	h.proxy(c, "/api/archive/conversations")
}

// Conversation returns one conversation including its transcript.
//
// This is the sensitive read, so it is logged with the subject's identity here
// as well as in the sidecar: an archive whose reads leave no trace is only half
// an audit system, and the gateway is where the reader is authenticated.
func (h *ArchiveHandler) Conversation(c *gin.Context) {
	id := strings.TrimSpace(c.Param("id"))
	fields := []zap.Field{
		zap.String("conversation_id", id),
		zap.String("client_ip", c.ClientIP()),
	}
	if subject, ok := middleware2.GetAuthSubjectFromContext(c); ok {
		fields = append(fields, zap.Int64("actor_user_id", subject.UserID))
	}
	if email := c.GetString(middleware2.ContextKeyAuthEmail); email != "" {
		fields = append(fields, zap.String("actor_email", email))
	}
	h.log().Info("admin.archive.transcript_read", fields...)
	h.proxy(c, "/api/archive/conversations/"+url.PathEscape(id))
}

// ConversationRequests returns the per-call timeline of one conversation.
func (h *ArchiveHandler) ConversationRequests(c *gin.Context) {
	id := strings.TrimSpace(c.Param("id"))
	h.proxy(c, "/api/archive/conversations/"+url.PathEscape(id)+"/requests")
}

// Users returns the per-user roll-up.
func (h *ArchiveHandler) Users(c *gin.Context) { h.proxy(c, "/api/archive/users") }

// Stats returns archive totals.
func (h *ArchiveHandler) Stats(c *gin.Context) { h.proxy(c, "/api/archive/stats") }

func (h *ArchiveHandler) proxy(c *gin.Context, path string) {
	if !h.cfg.Enabled() {
		c.JSON(http.StatusServiceUnavailable, gin.H{
			"error": "conversation archive is not configured",
			"code":  "archive_not_configured",
		})
		return
	}
	ctx, cancel := context.WithTimeout(c.Request.Context(),
		time.Duration(h.cfg.TimeoutMS)*time.Millisecond)
	defer cancel()

	body, status, err := h.fetch(ctx, path, c.Request.URL.RawQuery)
	if err != nil {
		h.log().Error("admin.archive.proxy_failed",
			zap.String("path", path), zap.Error(err))
		c.JSON(http.StatusBadGateway, gin.H{
			"error": "conversation archive is unreachable",
			"code":  "archive_unreachable",
		})
		return
	}
	// The sidecar already answers JSON, including for its own errors; passing
	// the status through keeps 404 meaning 404 in the console.
	c.Data(status, "application/json; charset=utf-8", body)
}

// fetch performs one upstream call. The sidecar token is attached here and
// never leaves the gateway.
func (h *ArchiveHandler) fetch(ctx context.Context, path, rawQuery string) ([]byte, int, error) {
	base := strings.TrimRight(strings.TrimSpace(h.cfg.SidecarURL), "/")
	target := base + path
	if rawQuery != "" {
		target += "?" + rawQuery
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, target, nil)
	if err != nil {
		return nil, 0, err
	}
	req.Header.Set("Authorization", "Bearer "+strings.TrimSpace(h.cfg.SidecarToken))
	req.Header.Set("Accept", "application/json")

	resp, err := h.client.Do(req)
	if err != nil {
		return nil, 0, err
	}
	defer func() { _ = resp.Body.Close() }()

	// A conversation can be megabytes; the cap is generous but bounded so a
	// misbehaving upstream cannot exhaust gateway memory.
	body, err := io.ReadAll(io.LimitReader(resp.Body, 64<<20))
	if err != nil {
		return nil, 0, err
	}
	return body, resp.StatusCode, nil
}
