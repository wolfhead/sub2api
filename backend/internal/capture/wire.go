package capture

import (
	"log/slog"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/google/wire"
	"github.com/redis/go-redis/v9"
)

// ProviderSet wires the capture side channel.
var ProviderSet = wire.NewSet(
	ProvideSink,
	ProvideService,
)

// ProvideSink returns the archive queue. The gateway's own Redis client is
// reused: capture adds a key, not an instance.
func ProvideSink(rdb *redis.Client, cfg *config.Config) Sink {
	return NewRedisSink(rdb, cfg.Capture.RedisKey, cfg.Capture.MaxQueueLength)
}

func ProvideService(cfg *config.Config, sink Sink) *Service {
	return NewService(cfg.Capture, sink, slog.Default())
}
