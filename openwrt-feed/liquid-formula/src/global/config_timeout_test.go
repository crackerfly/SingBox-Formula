package global

import (
	"strings"
	"testing"
)

func TestValidateRejectsWriteTimeoutAtOrBelowSubscriptionTimeout(t *testing.T) {
	for _, writeTimeout := range []int{29, 30} {
		config := validTimeoutConfig()
		config.Server.WriteTimeout = writeTimeout
		if err := config.Validate(); err == nil || !strings.Contains(err.Error(), "write_timeout") {
			t.Fatalf("Validate() with write_timeout=%d error = %v, want write_timeout error", writeTimeout, err)
		}
	}
}

func TestValidateAcceptsWriteTimeoutAboveSubscriptionTimeout(t *testing.T) {
	config := validTimeoutConfig()
	config.Server.WriteTimeout = 31
	if err := config.Validate(); err != nil {
		t.Fatalf("Validate() error = %v", err)
	}
}

func validTimeoutConfig() *Config {
	return &Config{
		Server:       ServerConfig{Port: 9716},
		Auth:         AuthConfig{Password: "890716"},
		Subscription: SubscriptionConfig{URL: "https://example.test/nodes", Timeout: 30, RefreshInterval: 1},
		Templates: map[string]TemplateConfig{
			"default": {Enabled: true},
		},
		DefaultTemplate: "default",
		Cache:           CacheConfig{Directory: "/tmp/cache"},
	}
}
