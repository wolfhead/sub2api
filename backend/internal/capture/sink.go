package capture

import (
	"context"

	"github.com/redis/go-redis/v9"
)

// Sink is where archived payloads go. It exists so the delivery mechanism can
// be swapped (and faked in tests) without touching the hot-path submit logic.
type Sink interface {
	// Send delivers one encoded payload. A returned error is counted and
	// logged by the caller; the payload is not retried.
	Send(ctx context.Context, payload []byte) error
	// Depth reports how many payloads are waiting to be consumed, for
	// backlog observability. Sinks that cannot answer return -1, nil.
	Depth(ctx context.Context) (int64, error)
	Name() string
}

// RedisSink pushes payloads onto a capped Redis list that sub2api-sidecar
// drains with BRPOP.
//
// Redis is an in-memory store shared with the gateway's own caches, so the list
// is hard-capped: when the consumer is down the queue must not be allowed to
// grow until it evicts gateway state or exhausts the instance. LTRIM keeps the
// newest MaxLen entries and silently discards the oldest — capture data is
// worth losing before the gateway is.
type RedisSink struct {
	rdb    *redis.Client
	key    string
	maxLen int64
}

func NewRedisSink(rdb *redis.Client, key string, maxLen int64) *RedisSink {
	return &RedisSink{rdb: rdb, key: key, maxLen: maxLen}
}

func (s *RedisSink) Name() string { return "redis:" + s.key }

func (s *RedisSink) Send(ctx context.Context, payload []byte) error {
	if s == nil || s.rdb == nil {
		return errSinkUnavailable
	}
	pipe := s.rdb.TxPipeline()
	pipe.LPush(ctx, s.key, payload)
	if s.maxLen > 0 {
		// Index maxLen-1 is inclusive, so this keeps exactly maxLen entries.
		pipe.LTrim(ctx, s.key, 0, s.maxLen-1)
	}
	_, err := pipe.Exec(ctx)
	return err
}

func (s *RedisSink) Depth(ctx context.Context) (int64, error) {
	if s == nil || s.rdb == nil {
		return -1, errSinkUnavailable
	}
	return s.rdb.LLen(ctx, s.key).Result()
}
