package config

import (
	"os"
	"testing"

	"strings"

	"github.com/spf13/viper"
)

// Deployment sets capture through environment variables only. viper decodes a
// key from the environment solely when that key exists in AllKeys(), which is
// why every capture setting registers a default. Prove it rather than assume.
func TestCaptureEnvBinding(t *testing.T) {
	viper.Reset()
	t.Cleanup(viper.Reset)

	for k, v := range map[string]string{
		"CAPTURE_ENABLED":                    "true",
		"CAPTURE_REDIS_KEY":                  "custom:queue",
		"CAPTURE_MAX_QUEUE_LENGTH":           "5000",
		"CAPTURE_MAX_INFLIGHT":               "64",
		"CAPTURE_SEND_TIMEOUT_MS":            "1500",
		"CAPTURE_DEPTH_LOG_INTERVAL_SECONDS": "30",
	} {
		t.Setenv(k, v)
	}
	_ = os.Setenv("SKIP_CONFIG_FILE", "1")

	viper.AutomaticEnv()
	viper.SetEnvKeyReplacer(strings.NewReplacer(".", "_"))
	setDefaults()

	var cfg Config
	if err := viper.Unmarshal(&cfg); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if !cfg.Capture.Enabled {
		t.Error("CAPTURE_ENABLED=true did not reach capture.enabled")
	}
	if cfg.Capture.RedisKey != "custom:queue" {
		t.Errorf("redis_key = %q", cfg.Capture.RedisKey)
	}
	if cfg.Capture.MaxQueueLength != 5000 {
		t.Errorf("max_queue_length = %d", cfg.Capture.MaxQueueLength)
	}
	if cfg.Capture.MaxInflight != 64 {
		t.Errorf("max_inflight = %d", cfg.Capture.MaxInflight)
	}
	if cfg.Capture.SendTimeoutMS != 1500 {
		t.Errorf("send_timeout_ms = %d", cfg.Capture.SendTimeoutMS)
	}
	if cfg.Capture.DepthLogIntervalSeconds != 30 {
		t.Errorf("depth_log_interval_seconds = %d", cfg.Capture.DepthLogIntervalSeconds)
	}
}

func TestCaptureDefaultsOff(t *testing.T) {
	viper.Reset()
	t.Cleanup(viper.Reset)
	setDefaults()
	var cfg Config
	if err := viper.Unmarshal(&cfg); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if cfg.Capture.Enabled {
		t.Error("capture is on by default; it must be opt-in")
	}
	if cfg.Capture.RedisKey != "sub2api:capture:queue" || cfg.Capture.MaxQueueLength != 20000 {
		t.Errorf("unexpected defaults: %+v", cfg.Capture)
	}
}
