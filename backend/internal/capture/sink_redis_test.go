package capture

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/redis/go-redis/v9"
)

// redisForTest connects to a real Redis, skipping when none is reachable.
// CAPTURE_TEST_REDIS_ADDR overrides the default address.
func redisForTest(t *testing.T) (*redis.Client, string) {
	t.Helper()
	addr := os.Getenv("CAPTURE_TEST_REDIS_ADDR")
	if addr == "" {
		addr = "127.0.0.1:6379"
	}
	client := redis.NewClient(&redis.Options{Addr: addr})
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	if err := client.Ping(ctx).Err(); err != nil {
		_ = client.Close()
		t.Skipf("no reachable Redis at %s: %v", addr, err)
	}
	key := fmt.Sprintf("test:capture:%d", time.Now().UnixNano())
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		_ = client.Del(ctx, key).Err()
		_ = client.Close()
	})
	return client, key
}

func TestRedisSinkPushesAndReportsDepth(t *testing.T) {
	client, key := redisForTest(t)
	sink := NewRedisSink(client, key, 100)
	ctx := context.Background()

	for i := 0; i < 3; i++ {
		if err := sink.Send(ctx, []byte(fmt.Sprintf(`{"n":%d}`, i))); err != nil {
			t.Fatalf("Send: %v", err)
		}
	}

	depth, err := sink.Depth(ctx)
	if err != nil {
		t.Fatalf("Depth: %v", err)
	}
	if depth != 3 {
		t.Fatalf("depth = %d, want 3", depth)
	}

	// The consumer drains with BRPOP, so the oldest payload must come off the
	// right-hand end: the queue is FIFO, not a stack.
	got, err := client.RPop(ctx, key).Bytes()
	if err != nil {
		t.Fatalf("RPop: %v", err)
	}
	var decoded struct {
		N int `json:"n"`
	}
	if err := json.Unmarshal(got, &decoded); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if decoded.N != 0 {
		t.Errorf("first payload out = %d, want 0 (FIFO order)", decoded.N)
	}
}

// Redis is shared with the gateway's own caches. When the consumer is down the
// queue must stop growing rather than consume the instance, so LTRIM caps it
// and the oldest archive entries are the ones sacrificed.
func TestRedisSinkCapsQueueLength(t *testing.T) {
	client, key := redisForTest(t)
	const maxLen = 5
	sink := NewRedisSink(client, key, maxLen)
	ctx := context.Background()

	for i := 0; i < 50; i++ {
		if err := sink.Send(ctx, []byte(fmt.Sprintf(`{"n":%d}`, i))); err != nil {
			t.Fatalf("Send %d: %v", i, err)
		}
	}

	depth, err := sink.Depth(ctx)
	if err != nil {
		t.Fatalf("Depth: %v", err)
	}
	if depth != maxLen {
		t.Fatalf("depth = %d, want the hard cap %d", depth, maxLen)
	}

	// The survivors must be the newest entries.
	oldest, err := client.RPop(ctx, key).Bytes()
	if err != nil {
		t.Fatalf("RPop: %v", err)
	}
	var decoded struct {
		N int `json:"n"`
	}
	if err := json.Unmarshal(oldest, &decoded); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if decoded.N != 50-maxLen {
		t.Errorf("oldest surviving payload = %d, want %d", decoded.N, 50-maxLen)
	}
}

func TestRedisSinkUnlimitedWhenMaxLenZero(t *testing.T) {
	client, key := redisForTest(t)
	sink := NewRedisSink(client, key, 0)
	ctx := context.Background()

	for i := 0; i < 20; i++ {
		if err := sink.Send(ctx, []byte(`{}`)); err != nil {
			t.Fatalf("Send %d: %v", i, err)
		}
	}
	depth, err := sink.Depth(ctx)
	if err != nil {
		t.Fatalf("Depth: %v", err)
	}
	if depth != 20 {
		t.Errorf("depth = %d, want 20 (no cap configured)", depth)
	}
}

func TestRedisSinkWithoutClientReportsUnavailable(t *testing.T) {
	var sink *RedisSink
	if err := sink.Send(context.Background(), []byte(`{}`)); err == nil {
		t.Error("Send on a nil sink returned no error")
	}
	if _, err := sink.Depth(context.Background()); err == nil {
		t.Error("Depth on a nil sink returned no error")
	}
}

// End-to-end for the gateway side: a securityaudit.Request goes in, a decoded
// archive payload comes off the queue exactly as the sidecar will read it.
func TestServiceDeliversDecodablePayloadToRedis(t *testing.T) {
	client, key := redisForTest(t)
	cfg := enabledConfig()
	cfg.RedisKey = key
	svc := NewService(cfg, NewRedisSink(client, key, 100), quietLogger())
	defer svc.Shutdown(context.Background())

	svc.Submit(chatRequest("req-e2e"))
	waitFor(t, func() bool {
		n, err := client.LLen(context.Background(), key).Result()
		return err == nil && n == 1
	})

	raw, err := client.RPop(context.Background(), key).Bytes()
	if err != nil {
		t.Fatalf("RPop: %v", err)
	}
	var payload Payload
	if err := json.Unmarshal(raw, &payload); err != nil {
		t.Fatalf("sidecar could not decode the payload: %v", err)
	}
	if payload.RequestID != "req-e2e" {
		t.Errorf("request_id = %q, want req-e2e", payload.RequestID)
	}
	if !strings.Contains(payload.Conversation.Text, "hello archive") {
		t.Errorf("prompt text = %q, want the transcript", payload.Conversation.Text)
	}
	if payload.Route.Endpoint != "/v1/chat/completions" {
		t.Errorf("endpoint = %q", payload.Route.Endpoint)
	}
}
