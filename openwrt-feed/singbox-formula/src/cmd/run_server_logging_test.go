package cmd

import (
	"testing"

	"github.com/haierkeys/singbox-subscribe-convert/global"
	"go.uber.org/zap"
	"go.uber.org/zap/zapcore"
	"go.uber.org/zap/zaptest/observer"
)

func TestLoggerConfigCarriesRotationSettings(t *testing.T) {
	input := global.LoggingConfig{
		Level:      "debug",
		File:       "/var/log/singbox-formula/server.log",
		Production: true,
		MaxSize:    12,
		MaxBackups: 5,
		MaxAge:     8,
	}
	got := loggerConfigFromGlobal(input)
	if got.Level != input.Level || got.File != input.File || got.Production != input.Production || got.MaxSize != 12 || got.MaxBackups != 5 || got.MaxAge != 8 {
		t.Fatalf("logger config = %+v, want values from %+v", got, input)
	}
}

func TestStartupLogRetainsCompleteSubscriptionURL(t *testing.T) {
	core, observed := observer.New(zapcore.InfoLevel)
	server := &Server{logger: zap.New(core)}
	secretURL := "https://user:password@example.invalid/sub?token=full-token&_t=123&_r=abcdef"
	config := &global.Config{
		Server:          global.ServerConfig{Port: 9716},
		Subscription:    global.SubscriptionConfig{URL: secretURL, RefreshInterval: 360},
		Cache:           global.CacheConfig{Directory: "/tmp/cache"},
		DefaultTemplate: "momo_template",
		Templates: map[string]global.TemplateConfig{
			"momo_template": {Enabled: true},
		},
	}
	server.logStartupInfo("/etc/singbox-formula/config.yaml", config)
	entries := observed.FilterMessage("Configuration loaded").All()
	if len(entries) != 1 {
		t.Fatalf("Configuration loaded entries = %d, want 1", len(entries))
	}
	if got := entries[0].ContextMap()["subscription_url"]; got != secretURL {
		t.Fatalf("subscription_url log = %#v, want complete %q", got, secretURL)
	}
}
