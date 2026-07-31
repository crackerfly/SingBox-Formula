package global

import (
	"os"
	"path/filepath"
	"testing"
)

func TestLoadCandidateDoesNotMutateGlobalConfigOnFailure(t *testing.T) {
	for _, name := range []string{
		"SERVER_PORT",
		"PASSWORD",
		"SUBSCRIPTION_URL",
		"DEFAULT_TEMPLATE",
		"CACHE_DIR",
		"REFRESH_INTERVAL",
	} {
		t.Setenv(name, "")
	}
	originalCfg := &Config{Auth: AuthConfig{Password: "last-known-good"}}
	originalPath := "/last-known-good/config.yaml"
	previousCfg, previousPath := Cfg, ConfigFile
	Cfg, ConfigFile = originalCfg, originalPath
	t.Cleanup(func() {
		Cfg, ConfigFile = previousCfg, previousPath
	})

	configPath := filepath.Join(t.TempDir(), "invalid.yaml")
	if err := os.WriteFile(configPath, []byte("server: [\n"), 0o600); err != nil {
		t.Fatalf("write invalid config: %v", err)
	}

	if _, _, err := LoadCandidate(configPath); err == nil {
		t.Fatal("LoadCandidate() error = nil, want invalid YAML error")
	}
	if Cfg != originalCfg {
		t.Fatal("LoadCandidate() replaced Cfg after failure")
	}
	if ConfigFile != originalPath {
		t.Fatalf("LoadCandidate() ConfigFile = %q, want %q", ConfigFile, originalPath)
	}

	validConfig := []byte(`
server:
  port: 9716
  write_timeout: 31
auth:
  password: test-password
subscription:
  url: https://example.com/subscription
  timeout: 30
  refresh_interval: 1
templates:
  default:
    enabled: true
default_template: default
cache:
  directory: /tmp/cache
`)
	if err := os.WriteFile(configPath, validConfig, 0o600); err != nil {
		t.Fatalf("write valid config: %v", err)
	}
	candidate, _, err := LoadCandidate(configPath)
	if err != nil {
		t.Fatalf("LoadCandidate() valid config error = %v", err)
	}
	if candidate == nil || candidate.Server.Port != 9716 {
		t.Fatalf("LoadCandidate() candidate = %#v, want port 9716", candidate)
	}
	if Cfg != originalCfg {
		t.Fatal("LoadCandidate() replaced Cfg after success")
	}
	if ConfigFile != originalPath {
		t.Fatalf("LoadCandidate() ConfigFile after success = %q, want %q", ConfigFile, originalPath)
	}
}
