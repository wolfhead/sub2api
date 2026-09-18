package capture

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"sync"
	"sync/atomic"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/Wei-Shaw/sub2api/internal/securityaudit"
)

var errSinkUnavailable = errors.New("capture sink unavailable")

// Service accepts audited gateway requests and delivers them to the archive
// sink off the request goroutine.
//
// Every guarantee here points the same way: the gateway must not slow down or
// fail because archiving is slow, backed up or broken. Submit never blocks,
// never returns an error and never propagates a panic. When the bounded
// in-flight budget is exhausted the payload is dropped — but a drop is always
// counted and logged, never silent.
type Service struct {
	enabled bool
	sink    Sink
	slots   chan struct{}
	timeout time.Duration
	log     *slog.Logger

	depthInterval time.Duration

	background context.Context
	cancel     context.CancelFunc
	wg         sync.WaitGroup

	startOnce sync.Once
	stopOnce  sync.Once

	submitted atomic.Int64
	skipped   atomic.Int64
	dropped   atomic.Int64
	failed    atomic.Int64
}

// Stats is a point-in-time counter snapshot, exposed for logging and ops.
type Stats struct {
	Submitted int64 `json:"submitted"`
	Skipped   int64 `json:"skipped"`
	Dropped   int64 `json:"dropped"`
	Failed    int64 `json:"failed"`
}

// NewService builds the capture service. A disabled config still yields a
// usable (inert) Service so callers never need a nil check beyond the one
// Submit already does.
func NewService(cfg config.CaptureConfig, sink Sink, logger *slog.Logger) *Service {
	if logger == nil {
		logger = slog.Default()
	}
	logger = logger.With("component", "capture")

	inflight := cfg.MaxInflight
	if inflight <= 0 {
		inflight = 256
	}
	timeout := time.Duration(cfg.SendTimeoutMS) * time.Millisecond
	if timeout <= 0 {
		timeout = 2 * time.Second
	}
	depthInterval := time.Duration(cfg.DepthLogIntervalSeconds) * time.Second
	if depthInterval <= 0 {
		depthInterval = time.Minute
	}

	ctx, cancel := context.WithCancel(context.Background())
	s := &Service{
		enabled:       cfg.Enabled && sink != nil,
		sink:          sink,
		slots:         make(chan struct{}, inflight),
		timeout:       timeout,
		log:           logger,
		depthInterval: depthInterval,
		background:    ctx,
		cancel:        cancel,
	}
	return s
}

func (s *Service) Enabled() bool { return s != nil && s.enabled }

func (s *Service) Stats() Stats {
	if s == nil {
		return Stats{}
	}
	return Stats{
		Submitted: s.submitted.Load(),
		Skipped:   s.skipped.Load(),
		Dropped:   s.dropped.Load(),
		Failed:    s.failed.Load(),
	}
}

// Start launches the backlog monitor. It is safe to call on a disabled service.
func (s *Service) Start() {
	if !s.Enabled() {
		return
	}
	s.startOnce.Do(func() {
		s.log.Info("capture enabled",
			"sink", s.sink.Name(),
			"max_inflight", cap(s.slots),
			"send_timeout", s.timeout.String())
		s.wg.Add(1)
		go s.monitor()
	})
}

// Shutdown drains in-flight deliveries, then releases the background context.
// Deliveries still running when ctx expires are abandoned.
func (s *Service) Shutdown(ctx context.Context) {
	if s == nil {
		return
	}
	s.stopOnce.Do(func() {
		done := make(chan struct{})
		go func() {
			s.wg.Wait()
			close(done)
		}()
		select {
		case <-done:
		case <-ctx.Done():
			s.log.Warn("capture shutdown timed out with deliveries in flight", "stats", s.Stats())
		}
		s.cancel()
	})
}

// Submit hands one audited request to the archive. It returns immediately.
//
// Callers must invoke it only after the gateway's own audit de-duplication has
// resolved (see handler.runSecurityAudit): capture inherits that de-duplication
// rather than reimplementing it, so one client request yields one payload.
func (s *Service) Submit(req securityaudit.Request) {
	if !s.Enabled() {
		return
	}
	select {
	case s.slots <- struct{}{}:
	default:
		dropped := s.dropped.Add(1)
		s.log.Warn("capture dropped: in-flight budget exhausted",
			"request_id", req.RequestID,
			"protocol", req.Protocol,
			"model", req.Model,
			"max_inflight", cap(s.slots),
			"dropped_total", dropped)
		return
	}

	// The handler owns req.Body and may reuse or release it once it returns.
	copied := req.Clone()
	s.wg.Add(1)
	go func() {
		defer s.wg.Done()
		defer func() { <-s.slots }()
		defer func() {
			if recovered := recover(); recovered != nil {
				s.failed.Add(1)
				// A panic value may embed prompt fragments; keep it out of logs.
				s.log.Error("capture delivery panicked",
					"request_id", copied.RequestID, "protocol", copied.Protocol)
			}
		}()
		s.deliver(copied)
	}()
}

func (s *Service) deliver(req securityaudit.Request) {
	payload, err := BuildPayload(req, time.Now())
	if err != nil {
		if errors.Is(err, securityaudit.ErrNoPromptText) {
			// Embeddings, image-only turns and similar bodies carry nothing to
			// archive. Expected, not a failure.
			s.skipped.Add(1)
			s.log.Debug("capture skipped: no prompt text",
				"request_id", req.RequestID, "protocol", req.Protocol, "endpoint", req.Endpoint)
			return
		}
		s.failed.Add(1)
		s.log.Warn("capture failed: payload build error",
			"request_id", req.RequestID, "protocol", req.Protocol, "err", err)
		return
	}

	encoded, err := json.Marshal(payload)
	if err != nil {
		s.failed.Add(1)
		s.log.Warn("capture failed: payload encode error",
			"request_id", req.RequestID, "protocol", req.Protocol, "err", err)
		return
	}

	ctx, cancel := context.WithTimeout(s.background, s.timeout)
	defer cancel()
	if err := s.sink.Send(ctx, encoded); err != nil {
		failed := s.failed.Add(1)
		s.log.Warn("capture failed: sink send error",
			"request_id", req.RequestID,
			"protocol", req.Protocol,
			"sink", s.sink.Name(),
			"payload_bytes", len(encoded),
			"failed_total", failed,
			"err", err)
		return
	}

	s.submitted.Add(1)
	s.log.Info("capture queued",
		"request_id", payload.RequestID,
		"user_id", payload.User.ID,
		"api_key_id", payload.APIKey.ID,
		"protocol", payload.Route.Protocol,
		"endpoint", payload.Route.Endpoint,
		"model", payload.Route.Model,
		"stage", payload.Stage,
		"prompt_chars", payload.Prompt.Chars,
		"messages", payload.Prompt.Messages,
		"payload_bytes", len(encoded))
}

// monitor periodically reports queue depth and counters so a stalled consumer
// is visible from the gateway logs alone, without querying Redis by hand.
func (s *Service) monitor() {
	defer s.wg.Done()
	ticker := time.NewTicker(s.depthInterval)
	defer ticker.Stop()
	for {
		select {
		case <-s.background.Done():
			return
		case <-ticker.C:
			ctx, cancel := context.WithTimeout(s.background, s.timeout)
			depth, err := s.sink.Depth(ctx)
			cancel()
			stats := s.Stats()
			if err != nil {
				s.log.Warn("capture backlog unknown", "sink", s.sink.Name(), "stats", stats, "err", err)
				continue
			}
			s.log.Info("capture backlog",
				"sink", s.sink.Name(),
				"queue_depth", depth,
				"submitted_total", stats.Submitted,
				"skipped_total", stats.Skipped,
				"dropped_total", stats.Dropped,
				"failed_total", stats.Failed)
		}
	}
}
