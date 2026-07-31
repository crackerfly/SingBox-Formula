package main

import (
	"bytes"
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"
)

const (
	configSecretCanary = "CONFIG_SECRET_CANARY_7f89a2"
	engineSecretCanary = "ENGINE_SECRET_CANARY_4c20bd"

	validGatewayYAML = `server:
  port: 9716
  write_timeout: 140
  converter_extension: 'accepted'
auth:
  password: '` + configSecretCanary + `'
subscription:
  url: 'http://127.0.0.1:9717/v1/aggregate'
  user_agent: 'sing-box/1.11 ` + configSecretCanary + `'
  timeout: 70
  refresh_interval: 360
templates:
  first:
    enabled: true
    url: 'http://127.0.0.1/first.json'
  second:
    enabled: false
  third:
    enabled: true
unknown_converter_root:
  remains: accepted
liquid_formula_gateway:
  listen_address: '127.0.0.1'
  listen_port: 9717
  source_timeout: 5
  aggregate_timeout: 70
  user_agent: 'sing-box/1.11 ` + configSecretCanary + `'
  urls:
    - 'https://one.invalid/sub?token=` + configSecretCanary + `'
    - 'http://two.invalid/path#` + configSecretCanary + `'
`
)

// This literal is intentionally independent of the implementation. It is the
// SHA256 of validGatewayYAML, including its final newline.
const validGatewayDigest = "fd84602b226ecce4e006c3ac0a2985aaa62a50176046999bba3f790bfd14cee5"

func TestReadGatewayConfigReadsOnceAndUsesThoseExactBytes(t *testing.T) {
	calls := 0
	poison := []byte("second read must never be parsed " + configSecretCanary)
	readFile := func(path string) ([]byte, error) {
		calls++
		if path != "/config/with-"+configSecretCanary+".yaml" {
			t.Fatalf("read path = %q", path)
		}
		if calls == 1 {
			return []byte(validGatewayYAML), nil
		}
		return poison, nil
	}

	config, err := readGatewayConfig(
		"/config/with-"+configSecretCanary+".yaml",
		validGatewayDigest,
		readFile,
	)
	if err != nil {
		t.Fatalf("readGatewayConfig: %v", err)
	}
	if calls != 1 {
		t.Fatalf("config reads = %d, want 1", calls)
	}
	if config.ConfigDigest != validGatewayDigest {
		t.Fatalf("config digest = %q", config.ConfigDigest)
	}
	if config.ListenAddress != "127.0.0.1" || config.ListenPort != 9717 {
		t.Fatalf("listen config = %s:%d", config.ListenAddress, config.ListenPort)
	}
	if config.SourceTimeoutSeconds != 5 ||
		config.AggregateTimeoutSeconds != 70 ||
		config.WriteTimeoutSeconds != 140 {
		t.Fatalf(
			"timeouts = source %d aggregate %d write %d",
			config.SourceTimeoutSeconds,
			config.AggregateTimeoutSeconds,
			config.WriteTimeoutSeconds,
		)
	}
	if config.UserAgent != "sing-box/1.11 "+configSecretCanary {
		t.Fatalf("user agent was not retained exactly")
	}
	if config.EnabledTemplates != 2 {
		t.Fatalf("enabled templates = %d, want 2", config.EnabledTemplates)
	}

	wantSources := []gatewaySource{
		{
			URL:       "https://one.invalid/sub?token=" + configSecretCanary,
			URLDigest: "865c52e8ce20517b31c3b9d9f2d618f3888886a8ca5f37d21ea4f29bd18710ab",
		},
		{
			URL:       "http://two.invalid/path#" + configSecretCanary,
			URLDigest: "ffad181db99f7025e556c222d8a13b95ece80983cf3faa6d716b5cdebca23db9",
		},
	}
	if len(config.Sources) != len(wantSources) {
		t.Fatalf("source occurrences = %d, want %d", len(config.Sources), len(wantSources))
	}
	for i := range wantSources {
		if config.Sources[i] != wantSources[i] {
			t.Fatalf("source %d = %#v, want %#v", i+1, config.Sources[i], wantSources[i])
		}
	}
}

func TestReadGatewayConfigHashesEveryExactByte(t *testing.T) {
	for name, raw := range map[string]string{
		"extra final newline": validGatewayYAML + "\n",
		"CRLF bytes":          strings.ReplaceAll(validGatewayYAML, "\n", "\r\n"),
		"trailing space":      strings.Replace(validGatewayYAML, "server:\n", "server: \n", 1),
	} {
		t.Run(name, func(t *testing.T) {
			_, err := readGatewayConfig("ignored", validGatewayDigest, func(string) ([]byte, error) {
				return []byte(raw), nil
			})
			if err == nil {
				t.Fatal("changed bytes accepted with stale digest")
			}
			assertNoCanary(t, err.Error())
		})
	}
}

func TestReadGatewayConfigRequiresLowercaseSHA256ExpectedDigest(t *testing.T) {
	cases := map[string]string{
		"empty":     "",
		"63 bytes":  strings.Repeat("a", 63),
		"65 bytes":  strings.Repeat("a", 65),
		"uppercase": strings.ToUpper(validGatewayDigest),
		"prefix LF": "\n" + validGatewayDigest,
		"suffix LF": validGatewayDigest + "\n",
		"space":     " " + validGatewayDigest,
		"non-hex":   strings.Repeat("g", 64),
	}
	for name, digest := range cases {
		t.Run(name, func(t *testing.T) {
			_, err := readGatewayConfig("ignored", digest, func(string) ([]byte, error) {
				return []byte(validGatewayYAML), nil
			})
			if err == nil {
				t.Fatalf("invalid expected digest %q accepted", digest)
			}
			assertNoCanary(t, err.Error())
		})
	}
}

func TestReadGatewayConfigRejectsDigestMismatchWithoutDisclosingConfig(t *testing.T) {
	_, err := readGatewayConfig(
		"/tmp/"+configSecretCanary,
		strings.Repeat("0", 64),
		func(string) ([]byte, error) {
			return []byte(validGatewayYAML), nil
		},
	)
	if err == nil {
		t.Fatal("digest mismatch accepted")
	}
	assertNoCanary(t, err.Error())
	assertNotContains(t, err.Error(), "https://")
	assertNotContains(t, err.Error(), "http://")
}

func TestGatewaySectionRequiresExactlyItsFrozenKeys(t *testing.T) {
	missingCases := map[string]struct {
		old         string
		replacement string
	}{
		"listen_address": {old: "  listen_address: '127.0.0.1'\n"},
		"listen_port":    {old: "  listen_port: 9717\n"},
		"source_timeout": {old: "  source_timeout: 5\n"},
		"aggregate_timeout": {
			old: "  aggregate_timeout: 70\n",
		},
		"user_agent": {
			old: "  user_agent: 'sing-box/1.11 " + configSecretCanary + "'\n" +
				"  urls:\n",
			replacement: "  urls:\n",
		},
		"urls": {
			old: "  urls:\n" +
				"    - 'https://one.invalid/sub?token=" + configSecretCanary + "'\n" +
				"    - 'http://two.invalid/path#" + configSecretCanary + "'\n",
		},
	}
	for name, change := range missingCases {
		t.Run("missing "+name, func(t *testing.T) {
			raw := replaceExactlyOnce(t, validGatewayYAML, change.old, change.replacement)
			assertGatewayConfigRejected(t, raw)
		})
	}

	t.Run("unknown gateway key", func(t *testing.T) {
		raw := replaceExactlyOnce(
			t,
			validGatewayYAML,
			"  listen_address: '127.0.0.1'\n",
			"  listen_address: '127.0.0.1'\n  "+configSecretCanary+": true\n",
		)
		assertGatewayConfigRejected(t, raw)
	})
}

func TestGatewayConfigIsOneStrictYAMLDocument(t *testing.T) {
	aliasURL := "https://alias.invalid/" + configSecretCanary
	cases := map[string]string{
		"second document": validGatewayYAML + "---\n" +
			"liquid_formula_gateway:\n  listen_address: '127.0.0.1'\n",
		"trailing malformed bytes": validGatewayYAML + "\x00" + configSecretCanary,
		"duplicate gateway key": replaceExactlyOnce(
			t,
			validGatewayYAML,
			"  source_timeout: 5\n",
			"  source_timeout: 5\n  source_timeout: 6\n",
		),
		"duplicate root section": validGatewayYAML +
			"liquid_formula_gateway:\n  listen_address: '127.0.0.1'\n",
		"merge key": "gateway_defaults: &gateway_defaults\n" +
			"  listen_address: '127.0.0.1'\n" +
			replaceExactlyOnce(
				t,
				validGatewayYAML,
				"  listen_address: '127.0.0.1'\n",
				"  <<: *gateway_defaults\n  listen_address: '127.0.0.1'\n",
			),
		"alias value": "aliased_urls: &aliased_urls\n  - '" + aliasURL + "'\n" +
			replaceExactlyOnce(
				t,
				validGatewayYAML,
				"  urls:\n"+
					"    - 'https://one.invalid/sub?token="+configSecretCanary+"'\n"+
					"    - 'http://two.invalid/path#"+configSecretCanary+"'\n",
				"  urls: *aliased_urls\n",
			),
		"string port": replaceExactlyOnce(
			t,
			validGatewayYAML,
			"  listen_port: 9717\n",
			"  listen_port: !!str 9717\n",
		),
		"float timeout": replaceExactlyOnce(
			t,
			validGatewayYAML,
			"  source_timeout: 5\n",
			"  source_timeout: 5.0\n",
		),
		"mapping urls": replaceExactlyOnce(
			t,
			validGatewayYAML,
			"  urls:\n"+
				"    - 'https://one.invalid/sub?token="+configSecretCanary+"'\n"+
				"    - 'http://two.invalid/path#"+configSecretCanary+"'\n",
			"  urls:\n    first: 'https://one.invalid/'\n",
		),
		"tagged address": replaceExactlyOnce(
			t,
			validGatewayYAML,
			"  listen_address: '127.0.0.1'\n",
			"  listen_address: !!binary MTI3LjAuMC4x\n",
		),
	}
	for name, raw := range cases {
		t.Run(name, func(t *testing.T) {
			assertGatewayConfigRejected(t, raw)
		})
	}
}

func TestGatewayConfigRequiresConverterAgreement(t *testing.T) {
	cases := map[string]string{
		"IPv4 loopback address": replaceExactlyOnce(
			t, validGatewayYAML,
			"  listen_address: '127.0.0.1'\n",
			"  listen_address: '0.0.0.0'\n",
		),
		"derived gateway port": replaceExactlyOnce(
			t, validGatewayYAML,
			"  listen_port: 9717\n",
			"  listen_port: 9718\n",
		),
		"loopback subscription host": replaceExactlyOnce(
			t, validGatewayYAML,
			"  url: 'http://127.0.0.1:9717/v1/aggregate'\n",
			"  url: 'http://localhost:9717/v1/aggregate'\n",
		),
		"loopback subscription path": replaceExactlyOnce(
			t, validGatewayYAML,
			"  url: 'http://127.0.0.1:9717/v1/aggregate'\n",
			"  url: 'http://127.0.0.1:9717/v1/aggregate/'\n",
		),
		"subscription aggregate timeout": replaceExactlyOnce(
			t, validGatewayYAML,
			"  timeout: 70\n",
			"  timeout: 69\n",
		),
		"gateway aggregate timeout": replaceExactlyOnce(
			t, validGatewayYAML,
			"  aggregate_timeout: 70\n",
			"  aggregate_timeout: 69\n",
		),
		"converter write timeout": replaceExactlyOnce(
			t, validGatewayYAML,
			"  write_timeout: 140\n",
			"  write_timeout: 139\n",
		),
		"user agent": replaceExactlyOnce(
			t, validGatewayYAML,
			"  aggregate_timeout: 70\n"+
				"  user_agent: 'sing-box/1.11 "+configSecretCanary+"'\n",
			"  aggregate_timeout: 70\n"+
				"  user_agent: 'different "+configSecretCanary+"'\n",
		),
	}
	for name, raw := range cases {
		t.Run(name, func(t *testing.T) {
			assertGatewayConfigRejected(t, raw)
		})
	}
}

func TestGatewayConfigValidatesURLOccurrencesAndSourceTimeout(t *testing.T) {
	nineURLs := "  urls:\n"
	for i := 1; i <= 9; i++ {
		nineURLs += fmt.Sprintf("    - 'https://%d.invalid/%s'\n", i, configSecretCanary)
	}
	nineURLConfig := replaceURLBlock(t, validGatewayYAML, nineURLs)
	nineURLConfig = replaceExactlyOnce(t, nineURLConfig, "  timeout: 70\n", "  timeout: 105\n")
	nineURLConfig = replaceExactlyOnce(
		t,
		nineURLConfig,
		"  aggregate_timeout: 70\n",
		"  aggregate_timeout: 105\n",
	)
	nineURLConfig = replaceExactlyOnce(
		t,
		nineURLConfig,
		"  write_timeout: 140\n",
		"  write_timeout: 175\n",
	)

	cases := map[string]string{
		"more than eight occurrences": nineURLConfig,
		"source timeout below five": strings.NewReplacer(
			"  source_timeout: 5\n", "  source_timeout: 4\n",
			"  timeout: 70\n", "  timeout: 68\n",
			"  aggregate_timeout: 70\n", "  aggregate_timeout: 68\n",
			"  write_timeout: 140\n", "  write_timeout: 136\n",
		).Replace(validGatewayYAML),
		"source timeout above six hundred": strings.NewReplacer(
			"  source_timeout: 5\n", "  source_timeout: 601\n",
			"  timeout: 70\n", "  timeout: 1262\n",
			"  aggregate_timeout: 70\n", "  aggregate_timeout: 1262\n",
			"  write_timeout: 140\n", "  write_timeout: 2524\n",
		).Replace(validGatewayYAML),
		"FTP URL": replaceExactlyOnce(
			t,
			validGatewayYAML,
			"https://one.invalid/sub?token="+configSecretCanary,
			"ftp://one.invalid/sub?token="+configSecretCanary,
		),
		"relative URL": replaceExactlyOnce(
			t,
			validGatewayYAML,
			"https://one.invalid/sub?token="+configSecretCanary,
			"/relative/"+configSecretCanary,
		),
		"HTTP URL without host": replaceExactlyOnce(
			t,
			validGatewayYAML,
			"https://one.invalid/sub?token="+configSecretCanary,
			"https:///"+configSecretCanary,
		),
		"HTTP URL with empty hostname": replaceExactlyOnce(
			t,
			validGatewayYAML,
			"https://one.invalid/sub?token="+configSecretCanary,
			"http://:80/"+configSecretCanary,
		),
		"HTTP URL with user and empty hostname": replaceExactlyOnce(
			t,
			validGatewayYAML,
			"https://one.invalid/sub?token="+configSecretCanary,
			"http://user@:80/"+configSecretCanary,
		),
		"URL with leading whitespace": replaceExactlyOnce(
			t,
			validGatewayYAML,
			"https://one.invalid/sub?token="+configSecretCanary,
			" https://one.invalid/sub?token="+configSecretCanary,
		),
		"URL with raw whitespace": replaceExactlyOnce(
			t,
			validGatewayYAML,
			"https://one.invalid/sub?token="+configSecretCanary,
			"https://one.invalid/raw space/"+configSecretCanary,
		),
		"URL with control character": replaceExactlyOnce(
			t,
			validGatewayYAML,
			"'https://one.invalid/sub?token="+configSecretCanary+"'",
			"\"https://one.invalid/sub\\x01token="+configSecretCanary+"\"",
		),
	}
	for name, raw := range cases {
		t.Run(name, func(t *testing.T) {
			assertGatewayConfigRejected(t, raw)
		})
	}
}

func TestGatewayConfigRejectsLeadingZeroDecimals(t *testing.T) {
	serverPort := replaceExactlyOnce(
		t,
		validGatewayYAML,
		"  port: 9716\n",
		"  port: 0716\n",
	)
	serverPort = strings.ReplaceAll(
		serverPort,
		"127.0.0.1:9717",
		"127.0.0.1:717",
	)
	serverPort = replaceExactlyOnce(
		t,
		serverPort,
		"  listen_port: 9717\n",
		"  listen_port: 717\n",
	)

	listenPort := replaceExactlyOnce(
		t,
		validGatewayYAML,
		"  port: 9716\n",
		"  port: 716\n",
	)
	listenPort = strings.ReplaceAll(
		listenPort,
		"127.0.0.1:9717",
		"127.0.0.1:717",
	)
	listenPort = replaceExactlyOnce(
		t,
		listenPort,
		"  listen_port: 9717\n",
		"  listen_port: 0717\n",
	)

	cases := map[string]string{
		"server port":         serverPort,
		"gateway listen port": listenPort,
		"source timeout": replaceExactlyOnce(
			t,
			validGatewayYAML,
			"  source_timeout: 5\n",
			"  source_timeout: 05\n",
		),
		"gateway aggregate timeout": replaceExactlyOnce(
			t,
			validGatewayYAML,
			"  aggregate_timeout: 70\n",
			"  aggregate_timeout: 070\n",
		),
		"server write timeout": replaceExactlyOnce(
			t,
			validGatewayYAML,
			"  write_timeout: 140\n",
			"  write_timeout: 0140\n",
		),
		"subscription timeout": replaceExactlyOnce(
			t,
			validGatewayYAML,
			"  timeout: 70\n",
			"  timeout: 070\n",
		),
	}
	for name, raw := range cases {
		t.Run(name, func(t *testing.T) {
			assertGatewayConfigRejected(t, raw)
		})
	}
}

func TestGatewayConfigValidatesUserAgentBytesAtBothFields(t *testing.T) {
	for name, value := range map[string]string{
		"newline":   "agent\nvalue",
		"control":   "agent\x1fvalue",
		"non ASCII": "agent-é",
		"201 bytes": strings.Repeat("A", 201),
	} {
		t.Run(name+" in both fields", func(t *testing.T) {
			raw := replaceSubscriptionUserAgent(t, validGatewayYAML, value)
			raw = replaceGatewayUserAgent(t, raw, value)
			assertGatewayConfigRejected(t, raw)
		})
		t.Run(name+" in subscription field", func(t *testing.T) {
			raw := replaceSubscriptionUserAgent(t, validGatewayYAML, value)
			assertGatewayConfigRejected(t, raw)
		})
		t.Run(name+" in gateway field", func(t *testing.T) {
			raw := replaceGatewayUserAgent(t, validGatewayYAML, value)
			assertGatewayConfigRejected(t, raw)
		})
	}

	t.Run("empty is allowed", func(t *testing.T) {
		raw := replaceSubscriptionUserAgent(t, validGatewayYAML, "")
		raw = replaceGatewayUserAgent(t, raw, "")
		config := readValidGatewayConfig(t, raw)
		if config.UserAgent != "" {
			t.Fatalf("user agent = %q, want empty", config.UserAgent)
		}
	})

	t.Run("200 printable ASCII bytes are allowed", func(t *testing.T) {
		value := strings.Repeat("A", 198) + " ~"
		raw := replaceSubscriptionUserAgent(t, validGatewayYAML, value)
		raw = replaceGatewayUserAgent(t, raw, value)
		config := readValidGatewayConfig(t, raw)
		if config.UserAgent != value {
			t.Fatalf("user agent length = %d, want 200", len(config.UserAgent))
		}
	})
}

func TestGatewayConfigRejectsAmbiguousTemplateEnabledValues(t *testing.T) {
	cases := map[string]string{
		"enabled string": replaceExactlyOnce(
			t,
			validGatewayYAML,
			"  first:\n    enabled: true\n",
			"  first:\n    enabled: 'true'\n",
		),
		"enabled alias": "enabled_value: &enabled_value true\n" +
			replaceExactlyOnce(
				t,
				validGatewayYAML,
				"  first:\n    enabled: true\n",
				"  first:\n    enabled: *enabled_value\n",
			),
		"template merge": "enabled_template: &enabled_template\n  enabled: true\n" +
			replaceExactlyOnce(
				t,
				validGatewayYAML,
				"  first:\n    enabled: true\n",
				"  first:\n    <<: *enabled_template\n",
			),
		"templates sequence": replaceExactlyOnce(
			t,
			validGatewayYAML,
			"templates:\n"+
				"  first:\n"+
				"    enabled: true\n"+
				"    url: 'http://127.0.0.1/first.json'\n"+
				"  second:\n"+
				"    enabled: false\n"+
				"  third:\n"+
				"    enabled: true\n",
			"templates:\n  - enabled: true\n  - enabled: true\n",
		),
	}
	for name, raw := range cases {
		t.Run(name, func(t *testing.T) {
			assertGatewayConfigRejected(t, raw)
		})
	}
}

func TestGatewayConfigAcceptsFrozenBoundaryCases(t *testing.T) {
	t.Run("disabled empty URL sequence uses S one", func(t *testing.T) {
		raw := replaceURLBlock(t, validGatewayYAML, "  urls: []\n")
		raw = strings.NewReplacer(
			"  timeout: 70\n", "  timeout: 65\n",
			"  aggregate_timeout: 70\n", "  aggregate_timeout: 65\n",
			"  write_timeout: 140\n", "  write_timeout: 135\n",
		).Replace(raw)
		config := readValidGatewayConfig(t, raw)
		if len(config.Sources) != 0 {
			t.Fatalf("disabled source occurrences = %d, want 0", len(config.Sources))
		}
	})

	t.Run("converter port 65535 maps to 65534", func(t *testing.T) {
		raw := strings.Replace(validGatewayYAML, "  port: 9716\n", "  port: 65535\n", 1)
		raw = strings.ReplaceAll(raw, "127.0.0.1:9717", "127.0.0.1:65534")
		raw = replaceExactlyOnce(t, raw, "  listen_port: 9717\n", "  listen_port: 65534\n")
		config := readValidGatewayConfig(t, raw)
		if config.ListenPort != 65534 {
			t.Fatalf("gateway port = %d, want 65534", config.ListenPort)
		}
	})

	t.Run("source timeout 600", func(t *testing.T) {
		raw := strings.NewReplacer(
			"  source_timeout: 5\n", "  source_timeout: 600\n",
			"  timeout: 70\n", "  timeout: 1260\n",
			"  aggregate_timeout: 70\n", "  aggregate_timeout: 1260\n",
			"  write_timeout: 140\n", "  write_timeout: 2520\n",
		).Replace(validGatewayYAML)
		config := readValidGatewayConfig(t, raw)
		if config.SourceTimeoutSeconds != 600 {
			t.Fatalf("source timeout = %d", config.SourceTimeoutSeconds)
		}
	})

	t.Run("ordered duplicate occurrences remain distinct", func(t *testing.T) {
		first := "https://one.invalid/sub?token=" + configSecretCanary
		raw := replaceExactlyOnce(
			t,
			validGatewayYAML,
			"http://two.invalid/path#"+configSecretCanary,
			first,
		)
		config := readValidGatewayConfig(t, raw)
		if len(config.Sources) != 2 {
			t.Fatalf("source occurrences = %d, want 2", len(config.Sources))
		}
		if config.Sources[0] != config.Sources[1] {
			t.Fatalf("duplicate occurrences changed: %#v", config.Sources)
		}
		if config.AggregateTimeoutSeconds != 70 {
			t.Fatalf("duplicates were not counted as occurrences: A=%d", config.AggregateTimeoutSeconds)
		}
	})
}

func TestCheckedInt32BudgetOperations(t *testing.T) {
	t.Run("addition exact maximum", func(t *testing.T) {
		got, err := checkedBudgetAdd(2147483600, 47)
		if err != nil || got != 2147483647 {
			t.Fatalf("checkedBudgetAdd = %d, %v", got, err)
		}
	})
	t.Run("addition overflow", func(t *testing.T) {
		if _, err := checkedBudgetAdd(2147483600, 48); err == nil {
			t.Fatal("int32 addition overflow accepted")
		}
	})
	t.Run("addition rejects negative operand", func(t *testing.T) {
		if _, err := checkedBudgetAdd(-1, 1); err == nil {
			t.Fatal("negative addition operand accepted")
		}
	})
	t.Run("multiplication below maximum", func(t *testing.T) {
		got, err := checkedBudgetMultiply(3579139, 600)
		if err != nil || got != 2147483400 {
			t.Fatalf("checkedBudgetMultiply = %d, %v", got, err)
		}
	})
	t.Run("multiplication overflow", func(t *testing.T) {
		if _, err := checkedBudgetMultiply(3579140, 600); err == nil {
			t.Fatal("int32 multiplication overflow accepted")
		}
	})
	t.Run("multiplication rejects negative operand", func(t *testing.T) {
		if _, err := checkedBudgetMultiply(1, -1); err == nil {
			t.Fatal("negative multiplication operand accepted")
		}
	})
}

func TestCalculateGatewayBudgetsUsesFrozenFormulaWithoutCaps(t *testing.T) {
	cases := []struct {
		name       string
		sources    int64
		timeout    int64
		templates  int64
		wantA      int64
		wantR      int64
		wantReject bool
	}{
		{name: "zero URLs still uses S one", sources: 0, timeout: 5, templates: 0, wantA: 65, wantR: 125},
		{name: "eight URLs and timeout 600", sources: 8, timeout: 600, templates: 0, wantA: 4860, wantR: 4920},
		{name: "enabled templates are not capped", sources: 1, timeout: 5, templates: 10000, wantA: 65, wantR: 50125},
		{
			name:      "exact int32 request maximum",
			sources:   1,
			timeout:   7,
			templates: 306783360,
			wantA:     67,
			wantR:     2147483647,
		},
		{
			name:       "final addition overflows int32",
			sources:    1,
			timeout:    7,
			templates:  306783361,
			wantReject: true,
		},
		{name: "nine URLs", sources: 9, timeout: 5, templates: 0, wantReject: true},
		{name: "timeout four", sources: 1, timeout: 4, templates: 0, wantReject: true},
		{name: "timeout 601", sources: 1, timeout: 601, templates: 0, wantReject: true},
		{name: "negative template count", sources: 1, timeout: 5, templates: -1, wantReject: true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			gotA, gotR, err := calculateGatewayBudgets(tc.sources, tc.timeout, tc.templates)
			if tc.wantReject {
				if err == nil {
					t.Fatalf("calculateGatewayBudgets = %d, %d, nil", gotA, gotR)
				}
				return
			}
			if err != nil {
				t.Fatalf("calculateGatewayBudgets: %v", err)
			}
			if gotA != tc.wantA || gotR != tc.wantR {
				t.Fatalf("calculateGatewayBudgets = %d, %d, want %d, %d", gotA, gotR, tc.wantA, tc.wantR)
			}
		})
	}
}

func TestServeCLIUsageAndConfigErrorsAreSafe(t *testing.T) {
	usageCases := [][]string{
		{"serve"},
		{"serve", "--config", "/tmp/" + configSecretCanary},
		{"serve", "--expected-digest", validGatewayDigest},
		{"serve", "--config", "/tmp/" + configSecretCanary, "--expected-digest", strings.Repeat("a", 63)},
		{"serve", "--config", "/tmp/" + configSecretCanary, "--expected-digest", strings.ToUpper(validGatewayDigest)},
		{"serve", "--config", "/tmp/" + configSecretCanary, "--expected-digest", validGatewayDigest, "extra"},
		{"serve", "--config", "/tmp/" + configSecretCanary, "--expected-digest", validGatewayDigest, "--unknown"},
	}
	for _, args := range usageCases {
		t.Run(strings.Join(args, "_"), func(t *testing.T) {
			var stderr bytes.Buffer
			exit := run(args, strings.NewReader(""), io.Discard, &stderr)
			if exit != 2 {
				t.Fatalf("exit = %d, want 2; stderr=%q", exit, stderr.String())
			}
			assertSafeDiagnostic(t, stderr.String())
		})
	}

	t.Run("unreadable config", func(t *testing.T) {
		var stderr bytes.Buffer
		exit := run(
			[]string{
				"serve",
				"--config", filepath.Join(t.TempDir(), configSecretCanary),
				"--expected-digest", validGatewayDigest,
			},
			strings.NewReader(""),
			io.Discard,
			&stderr,
		)
		if exit != 1 {
			t.Fatalf("exit = %d, want 1; stderr=%q", exit, stderr.String())
		}
		assertSafeDiagnostic(t, stderr.String())
	})

	t.Run("digest mismatch", func(t *testing.T) {
		path := writeConfigFile(t, validGatewayYAML)
		var stderr bytes.Buffer
		exit := run(
			[]string{"serve", "--config", path, "--expected-digest", strings.Repeat("0", 64)},
			strings.NewReader(""),
			io.Discard,
			&stderr,
		)
		if exit != 1 {
			t.Fatalf("exit = %d, want 1; stderr=%q", exit, stderr.String())
		}
		assertSafeDiagnostic(t, stderr.String())
	})
}

func TestServeValidatesConfigBeforeOpeningListener(t *testing.T) {
	listenCalled := false
	dependencies := serveDependencies{
		ReadFile: func(string) ([]byte, error) {
			return []byte(validGatewayYAML), nil
		},
		Listen: func(string, string) (net.Listener, error) {
			listenCalled = true
			return nil, errors.New(engineSecretCanary)
		},
		NewEngine: func(gatewayConfig) aggregateEngine {
			t.Fatal("engine constructed before digest and config validation")
			return nil
		},
	}
	var stderr bytes.Buffer
	exit := runServe(
		[]string{
			"--config", "/tmp/" + configSecretCanary,
			"--expected-digest", strings.Repeat("0", 64),
		},
		&stderr,
		dependencies,
	)
	if exit != 1 {
		t.Fatalf("exit = %d, want 1; stderr=%q", exit, stderr.String())
	}
	if listenCalled {
		t.Fatal("listener was opened before digest and config validation")
	}
	assertSafeDiagnostic(t, stderr.String())
}

func TestServeConstructsEngineFromValidatedConfig(t *testing.T) {
	engineCalls := 0
	listenCalls := 0
	dependencies := serveDependencies{
		ReadFile: func(string) ([]byte, error) {
			return []byte(validGatewayYAML), nil
		},
		Listen: func(network, address string) (net.Listener, error) {
			listenCalls++
			if network != "tcp4" || address != "127.0.0.1:9717" {
				t.Fatalf("listen = %q %q", network, address)
			}
			return nil, errors.New(engineSecretCanary)
		},
		NewEngine: func(config gatewayConfig) aggregateEngine {
			engineCalls++
			if config.ConfigDigest != validGatewayDigest ||
				config.SourceTimeoutSeconds != 5 ||
				len(config.Sources) != 2 ||
				config.Sources[0].URLDigest !=
					"865c52e8ce20517b31c3b9d9f2d618f3888886a8ca5f37d21ea4f29bd18710ab" {
				t.Fatalf("engine received unvalidated config: %#v", config)
			}
			return staticAggregateEngine{
				outcome: aggregateOutcome{
					Code:        aggregateCodeSourceUnavailable,
					SourceIndex: 1,
				},
			}
		},
	}
	var stderr bytes.Buffer
	exit := runServe(
		[]string{
			"--config", "/tmp/" + configSecretCanary,
			"--expected-digest", validGatewayDigest,
		},
		&stderr,
		dependencies,
	)
	if exit != 1 {
		t.Fatalf("exit = %d, want 1; stderr=%q", exit, stderr.String())
	}
	if engineCalls != 1 || listenCalls != 1 {
		t.Fatalf("engine calls = %d, listen calls = %d", engineCalls, listenCalls)
	}
	assertSafeDiagnostic(t, stderr.String())
}

func TestServeDoesNotFallBackWhenConfiguredListenerIsOccupied(t *testing.T) {
	occupied, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("occupy loopback listener: %v", err)
	}
	t.Cleanup(func() {
		if err := occupied.Close(); err != nil {
			t.Errorf("occupied.Close: %v", err)
		}
	})
	gatewayPort := occupied.Addr().(*net.TCPAddr).Port
	if gatewayPort < 2 {
		t.Fatalf("unexpected occupied port %d", gatewayPort)
	}
	raw := gatewayYAMLForPorts(t, gatewayPort-1, gatewayPort)
	digest := testDigest([]byte(raw))

	var stderr bytes.Buffer
	exit := runServe(
		[]string{
			"--config", "/tmp/" + configSecretCanary,
			"--expected-digest", digest,
		},
		&stderr,
		serveDependencies{
			ReadFile: func(string) ([]byte, error) {
				return []byte(raw), nil
			},
			Listen: net.Listen,
			NewEngine: func(gatewayConfig) aggregateEngine {
				return staticAggregateEngine{
					outcome: aggregateOutcome{
						Code:        aggregateCodeSourceUnavailable,
						SourceIndex: 1,
					},
				}
			},
		},
	)
	if exit != 1 {
		t.Fatalf("exit = %d, want 1; stderr=%q", exit, stderr.String())
	}
	assertSafeDiagnostic(t, stderr.String())
}

func TestNormalizeCLIStillWorksAfterServeIsAdded(t *testing.T) {
	var stdout bytes.Buffer
	var stderr bytes.Buffer
	exit := run(
		[]string{"normalize"},
		strings.NewReader(`{"outbounds":[{"type":"direct","tag":"direct"}]}`),
		&stdout,
		&stderr,
	)
	if exit != 0 {
		t.Fatalf("normalize exit = %d; stderr=%q", exit, stderr.String())
	}
	if !strings.Contains(stdout.String(), `"outbounds"`) {
		t.Fatalf("normalize output = %q", stdout.String())
	}
}

func TestGatewayUsesRealIPv4LoopbackListenerAndExactHTTPContract(t *testing.T) {
	config := gatewayConfigForAvailablePort(t)
	engine := deadlineCheckingUnavailableEngine{
		want: time.Duration(config.AggregateTimeoutSeconds) * time.Second,
	}
	server, listener := startGatewayTestServer(t, config, engine)

	if server.WriteTimeout != time.Duration(config.WriteTimeoutSeconds)*time.Second {
		t.Fatalf("server write timeout = %s", server.WriteTimeout)
	}
	tcpAddress, ok := listener.Addr().(*net.TCPAddr)
	if !ok {
		t.Fatalf("listener address type = %T", listener.Addr())
	}
	if tcpAddress.IP.String() != "127.0.0.1" || tcpAddress.IP.To4() == nil {
		t.Fatalf("listener address = %s, want IPv4 127.0.0.1", tcpAddress)
	}
	ipv6Address := net.JoinHostPort("::1", strconv.Itoa(tcpAddress.Port))
	if connection, err := net.DialTimeout("tcp6", ipv6Address, 100*time.Millisecond); err == nil {
		connection.Close()
		t.Fatalf("gateway also accepted IPv6 on %s", ipv6Address)
	}

	baseURL := "http://" + listener.Addr().String()
	health := doRequest(t, http.MethodGet, baseURL+"/health?ignored="+configSecretCanary)
	assertResponse(
		t,
		health,
		http.StatusOK,
		"application/json",
		`{"service":"liquid-formula-subscription-gateway","status":"ok","config_digest":"`+
			config.ConfigDigest+`"}`,
	)

	aggregate := doRequest(t, http.MethodGet, baseURL+"/v1/aggregate")
	assertResponse(
		t,
		aggregate,
		http.StatusBadGateway,
		"application/json",
		`{"service":"liquid-formula-subscription-gateway","status":"error","code":"source_unavailable","source_index":1,"preserved":false}`,
	)
	if got := aggregate.header.Get("Cache-Control"); got != "no-store" {
		t.Fatalf("aggregate Cache-Control = %q, want no-store", got)
	}

	for _, request := range []struct {
		method string
		path   string
		status int
		allow  string
	}{
		{method: http.MethodHead, path: "/health", status: http.StatusMethodNotAllowed, allow: "GET"},
		{method: http.MethodPost, path: "/health", status: http.StatusMethodNotAllowed, allow: "GET"},
		{method: http.MethodPost, path: "/v1/aggregate", status: http.StatusMethodNotAllowed, allow: "GET"},
		{method: http.MethodGet, path: "/health/", status: http.StatusNotFound},
		{method: http.MethodGet, path: "/" + configSecretCanary, status: http.StatusNotFound},
		{method: http.MethodPost, path: "/" + configSecretCanary, status: http.StatusNotFound},
	} {
		response := doRequest(t, request.method, baseURL+request.path)
		assertResponse(t, response, request.status, "", "")
		if got := response.header.Get("Allow"); got != request.allow {
			t.Fatalf("%s %s Allow = %q, want %q", request.method, request.path, got, request.allow)
		}
	}
}

func TestGatewayMapsEveryTypedAggregateOutcomeToSafeHTTP(t *testing.T) {
	cases := []struct {
		name     string
		outcome  aggregateOutcome
		status   int
		wantBody string
	}{
		{
			name:     "no sources",
			outcome:  aggregateOutcome{Code: aggregateCodeNoSources},
			status:   http.StatusServiceUnavailable,
			wantBody: `{"service":"liquid-formula-subscription-gateway","status":"error","code":"no_sources","source_index":0,"preserved":false}`,
		},
		{
			name: "busy with preserved generation",
			outcome: aggregateOutcome{
				Code:      aggregateCodeBusy,
				Preserved: true,
			},
			status:   http.StatusServiceUnavailable,
			wantBody: `{"service":"liquid-formula-subscription-gateway","status":"error","code":"busy","source_index":0,"preserved":true}`,
		},
		{
			name: "source unavailable",
			outcome: aggregateOutcome{
				Code:         aggregateCodeSourceUnavailable,
				SourceIndex:  1,
				Preserved:    true,
				FailureStage: failureStageSourceFetch,
				FetchCode:    fetchCodeHTTPStatus,
			},
			status:   http.StatusBadGateway,
			wantBody: `{"service":"liquid-formula-subscription-gateway","status":"error","code":"source_unavailable","failure_stage":"source_fetch","fetch_code":"http_status","source_index":1,"preserved":true}`,
		},
		{
			name: "state invalid",
			outcome: aggregateOutcome{
				Code:      aggregateCodeStateInvalid,
				Preserved: true,
			},
			status:   http.StatusInternalServerError,
			wantBody: `{"service":"liquid-formula-subscription-gateway","status":"error","code":"state_invalid","source_index":0,"preserved":true}`,
		},
		{
			name:     "aggregate invalid",
			outcome:  aggregateOutcome{Code: aggregateCodeAggregateInvalid},
			status:   http.StatusInternalServerError,
			wantBody: `{"service":"liquid-formula-subscription-gateway","status":"error","code":"aggregate_invalid","source_index":0,"preserved":false}`,
		},
		{
			name: "commit failed",
			outcome: aggregateOutcome{
				Code:      aggregateCodeCommitFailed,
				Preserved: true,
			},
			status:   http.StatusInternalServerError,
			wantBody: `{"service":"liquid-formula-subscription-gateway","status":"error","code":"commit_failed","source_index":0,"preserved":true}`,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			config := gatewayConfigForAvailablePort(t)
			_, listener := startGatewayTestServer(t, config, staticAggregateEngine{outcome: tc.outcome})
			response := doRequest(
				t,
				http.MethodGet,
				"http://"+listener.Addr().String()+"/v1/aggregate",
			)
			assertResponse(t, response, tc.status, "application/json", tc.wantBody)
			if got := response.header.Get("Cache-Control"); got != "no-store" {
				t.Fatalf("Cache-Control = %q, want no-store", got)
			}
		})
	}
}

func TestGatewayRejectsUnlistedFailureDiagnosticsWithoutEchoingThem(t *testing.T) {
	config := gatewayConfigForAvailablePort(t)
	_, listener := startGatewayTestServer(
		t,
		config,
		staticAggregateEngine{outcome: aggregateOutcome{
			Code:         aggregateCodeSourceUnavailable,
			SourceIndex:  1,
			Preserved:    true,
			FailureStage: failureStage(configSecretCanary),
			FetchCode:    sourceFetchCode(configSecretCanary),
		}},
	)
	response := doRequest(
		t,
		http.MethodGet,
		"http://"+listener.Addr().String()+"/v1/aggregate",
	)
	assertCapturedResponseHasNoCanary(t, response)
	assertResponse(
		t,
		response,
		http.StatusInternalServerError,
		"application/json",
		`{"service":"liquid-formula-subscription-gateway","status":"error","code":"state_invalid","source_index":0,"preserved":false}`,
	)
}

func TestGatewayServesSuccessfulAggregateBytesExactly(t *testing.T) {
	config := gatewayConfigForAvailablePort(t)
	aggregate := []byte(`{"outbounds":[{"type":"direct","tag":"exact"}]}`)
	_, listener := startGatewayTestServer(
		t,
		config,
		staticAggregateEngine{outcome: aggregateOutcome{Bytes: aggregate}},
	)

	response := doRequest(t, http.MethodGet, "http://"+listener.Addr().String()+"/v1/aggregate")
	assertResponse(t, response, http.StatusOK, "application/json", string(aggregate))
	if got := response.header.Get("Cache-Control"); got != "no-store" {
		t.Fatalf("Cache-Control = %q, want no-store", got)
	}
}

func TestGatewayContainsEnginePanicAsSafeHTTPFailure(t *testing.T) {
	config := gatewayConfigForAvailablePort(t)
	errorLog := &lockedBuffer{}
	_, listener := startGatewayTestServerWithErrorLog(
		t,
		config,
		panickingAggregateEngine{},
		errorLog,
	)

	response, requestErr := performRequest(
		&http.Client{Timeout: 2 * time.Second},
		http.MethodGet,
		"http://"+listener.Addr().String()+"/v1/aggregate",
	)
	if requestErr != nil {
		t.Errorf("panic request returned a transport error: %v", requestErr)
	}
	assertSafeDiagnostic(t, errorLog.String())
	if requestErr != nil {
		return
	}
	assertCapturedResponseHasNoCanary(t, response)
	assertResponse(
		t,
		response,
		http.StatusInternalServerError,
		"application/json",
		`{"service":"liquid-formula-subscription-gateway","status":"error","code":"state_invalid","source_index":0,"preserved":false}`,
	)
	if got := response.header.Get("Cache-Control"); got != "no-store" {
		t.Fatalf("Cache-Control = %q, want no-store", got)
	}
}

func TestGatewayDeadlineWinsWhenEngineIgnoresContext(t *testing.T) {
	config := gatewayConfigForAvailablePort(t)
	config.AggregateTimeoutSeconds = 1
	finished := make(chan struct{})
	_, listener := startGatewayTestServer(
		t,
		config,
		slowIgnoringContextEngine{
			delay:    2 * time.Second,
			finished: finished,
		},
	)

	started := time.Now()
	response, err := performRequest(
		&http.Client{Timeout: 3 * time.Second},
		http.MethodGet,
		"http://"+listener.Addr().String()+"/v1/aggregate",
	)
	elapsed := time.Since(started)
	if err != nil {
		t.Fatalf("deadline request: %v", err)
	}
	assertCapturedResponseHasNoCanary(t, response)
	if elapsed > 1500*time.Millisecond {
		t.Errorf("deadline response took %s, want no more than 1.5s", elapsed)
	}
	assertResponse(
		t,
		response,
		http.StatusInternalServerError,
		"application/json",
		`{"service":"liquid-formula-subscription-gateway","status":"error","code":"state_invalid","failure_stage":"deadline","source_index":0,"preserved":false}`,
	)
	if got := response.header.Get("Cache-Control"); got != "no-store" {
		t.Fatalf("Cache-Control = %q, want no-store", got)
	}
	select {
	case <-finished:
	case <-time.After(2 * time.Second):
		t.Fatal("context-ignoring engine did not finish")
	}
}

func TestGatewayRejectsContradictorySuccessfulOutcome(t *testing.T) {
	aggregate := []byte(`{"outbounds":[{"type":"direct","tag":"contradictory"}]}`)
	for name, outcome := range map[string]aggregateOutcome{
		"nonzero source index": {
			Bytes:       aggregate,
			SourceIndex: 1,
		},
		"preserved flag": {
			Bytes:     aggregate,
			Preserved: true,
		},
		"index and preserved": {
			Bytes:       aggregate,
			SourceIndex: -1,
			Preserved:   true,
		},
	} {
		t.Run(name, func(t *testing.T) {
			config := gatewayConfigForAvailablePort(t)
			_, listener := startGatewayTestServer(
				t,
				config,
				staticAggregateEngine{outcome: outcome},
			)
			response := doRequest(
				t,
				http.MethodGet,
				"http://"+listener.Addr().String()+"/v1/aggregate",
			)
			assertResponse(
				t,
				response,
				http.StatusInternalServerError,
				"application/json",
				`{"service":"liquid-formula-subscription-gateway","status":"error","code":"state_invalid","source_index":0,"preserved":false}`,
			)
		})
	}
}

func TestGatewayFailsClosedOnEmptySuccessfulOutcome(t *testing.T) {
	for _, outcome := range []aggregateOutcome{
		{},
		{Bytes: []byte{}},
	} {
		config := gatewayConfigForAvailablePort(t)
		_, listener := startGatewayTestServer(
			t,
			config,
			staticAggregateEngine{outcome: outcome},
		)
		response := doRequest(
			t,
			http.MethodGet,
			"http://"+listener.Addr().String()+"/v1/aggregate",
		)
		assertResponse(
			t,
			response,
			http.StatusInternalServerError,
			"application/json",
			`{"service":"liquid-formula-subscription-gateway","status":"error","code":"state_invalid","source_index":0,"preserved":false}`,
		)
	}
}

func TestGatewayFailsClosedOnStructurallyInvalidKnownOutcome(t *testing.T) {
	cases := []struct {
		name    string
		outcome aggregateOutcome
	}{
		{
			name: "no sources with source index",
			outcome: aggregateOutcome{
				Code:        aggregateCodeNoSources,
				SourceIndex: 1,
				Preserved:   true,
			},
		},
		{
			name: "busy with negative source index",
			outcome: aggregateOutcome{
				Code:        aggregateCodeBusy,
				SourceIndex: -1,
				Preserved:   true,
			},
		},
		{
			name: "state invalid with source index",
			outcome: aggregateOutcome{
				Code:        aggregateCodeStateInvalid,
				SourceIndex: 2,
				Preserved:   true,
			},
		},
		{
			name: "aggregate invalid with source index",
			outcome: aggregateOutcome{
				Code:        aggregateCodeAggregateInvalid,
				SourceIndex: 1,
				Preserved:   true,
			},
		},
		{
			name: "commit failed with source index",
			outcome: aggregateOutcome{
				Code:        aggregateCodeCommitFailed,
				SourceIndex: 1,
				Preserved:   true,
			},
		},
		{
			name: "source unavailable with zero index",
			outcome: aggregateOutcome{
				Code:      aggregateCodeSourceUnavailable,
				Preserved: true,
			},
		},
		{
			name: "source unavailable with negative index",
			outcome: aggregateOutcome{
				Code:        aggregateCodeSourceUnavailable,
				SourceIndex: -1,
				Preserved:   true,
			},
		},
		{
			name: "source unavailable past configured occurrences",
			outcome: aggregateOutcome{
				Code:        aggregateCodeSourceUnavailable,
				SourceIndex: 3,
				Preserved:   true,
			},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			config := gatewayConfigForAvailablePort(t)
			_, listener := startGatewayTestServer(
				t,
				config,
				staticAggregateEngine{outcome: tc.outcome},
			)
			response := doRequest(
				t,
				http.MethodGet,
				"http://"+listener.Addr().String()+"/v1/aggregate",
			)
			assertResponse(
				t,
				response,
				http.StatusInternalServerError,
				"application/json",
				`{"service":"liquid-formula-subscription-gateway","status":"error","code":"state_invalid","source_index":0,"preserved":false}`,
			)
		})
	}
}

func TestGatewayFailsClosedOnUnknownEngineOutcome(t *testing.T) {
	config := gatewayConfigForAvailablePort(t)
	engine := staticAggregateEngine{
		outcome: aggregateOutcome{
			Bytes:       []byte(engineSecretCanary),
			Code:        aggregateFailureCode(engineSecretCanary),
			SourceIndex: 73,
			Preserved:   true,
		},
	}
	_, listener := startGatewayTestServer(t, config, engine)

	response := doRequest(t, http.MethodGet, "http://"+listener.Addr().String()+"/v1/aggregate")
	assertResponse(
		t,
		response,
		http.StatusInternalServerError,
		"application/json",
		`{"service":"liquid-formula-subscription-gateway","status":"error","code":"state_invalid","source_index":0,"preserved":false}`,
	)
}

type staticAggregateEngine struct {
	outcome aggregateOutcome
}

func (engine staticAggregateEngine) Aggregate(context.Context) aggregateOutcome {
	return engine.outcome
}

type panickingAggregateEngine struct{}

func (panickingAggregateEngine) Aggregate(context.Context) aggregateOutcome {
	panic(
		engineSecretCanary +
			" https://panic.invalid/?token=" +
			configSecretCanary,
	)
}

type slowIgnoringContextEngine struct {
	delay    time.Duration
	finished chan<- struct{}
}

func (engine slowIgnoringContextEngine) Aggregate(context.Context) aggregateOutcome {
	time.Sleep(engine.delay)
	close(engine.finished)
	return aggregateOutcome{
		Bytes: []byte(`{"outbounds":[{"type":"direct","tag":"late"}]}`),
	}
}

type deadlineCheckingUnavailableEngine struct {
	want time.Duration
}

func (engine deadlineCheckingUnavailableEngine) Aggregate(ctx context.Context) aggregateOutcome {
	deadline, ok := ctx.Deadline()
	remaining := time.Until(deadline)
	if !ok || remaining > engine.want || remaining < engine.want-2*time.Second {
		return aggregateOutcome{
			Bytes: []byte(engineSecretCanary),
			Code:  aggregateFailureCode(engineSecretCanary),
		}
	}
	return aggregateOutcome{
		Code:        aggregateCodeSourceUnavailable,
		SourceIndex: 1,
	}
}

type capturedResponse struct {
	status int
	header http.Header
	body   string
}

type lockedBuffer struct {
	mu     sync.Mutex
	buffer bytes.Buffer
}

func (buffer *lockedBuffer) Write(value []byte) (int, error) {
	buffer.mu.Lock()
	defer buffer.mu.Unlock()
	return buffer.buffer.Write(value)
}

func (buffer *lockedBuffer) String() string {
	buffer.mu.Lock()
	defer buffer.mu.Unlock()
	return buffer.buffer.String()
}

func startGatewayTestServer(
	t *testing.T,
	config gatewayConfig,
	engine aggregateEngine,
) (*http.Server, net.Listener) {
	t.Helper()
	return startGatewayTestServerWithErrorLog(
		t,
		config,
		engine,
		io.Discard,
	)
}

func startGatewayTestServerWithErrorLog(
	t *testing.T,
	config gatewayConfig,
	engine aggregateEngine,
	errorLog io.Writer,
) (*http.Server, net.Listener) {
	t.Helper()
	server, listener, err := openGatewayServer(config, engine, net.Listen)
	if err != nil {
		t.Fatalf("openGatewayServer: %v", err)
	}
	server.ErrorLog = log.New(errorLog, "", 0)
	serveResult := make(chan error, 1)
	go func() {
		serveResult <- server.Serve(listener)
	}()
	t.Cleanup(func() {
		err := server.Close()
		if err != nil {
			t.Errorf("server.Close: %v", err)
		}
		select {
		case err := <-serveResult:
			if err != nil && !errors.Is(err, http.ErrServerClosed) {
				t.Errorf("server.Serve: %v", err)
			}
		case <-time.After(2 * time.Second):
			t.Error("server did not stop")
		}
	})
	return server, listener
}

func doRequest(t *testing.T, method, target string) capturedResponse {
	t.Helper()
	response, err := performRequest(
		&http.Client{Timeout: 2 * time.Second},
		method,
		target,
	)
	if err != nil {
		t.Fatalf("HTTP %s %s: %v", method, target, err)
	}
	assertCapturedResponseHasNoCanary(t, response)
	return response
}

func performRequest(
	client *http.Client,
	method string,
	target string,
) (capturedResponse, error) {
	request, err := http.NewRequest(method, target, nil)
	if err != nil {
		return capturedResponse{}, err
	}
	response, err := client.Do(request)
	if err != nil {
		return capturedResponse{}, err
	}
	defer response.Body.Close()
	body, err := io.ReadAll(response.Body)
	if err != nil {
		return capturedResponse{}, err
	}
	return capturedResponse{
		status: response.StatusCode,
		header: response.Header.Clone(),
		body:   string(body),
	}, nil
}

func assertCapturedResponseHasNoCanary(
	t *testing.T,
	response capturedResponse,
) {
	t.Helper()
	all := response.body
	for name, values := range response.header {
		all += name + strings.Join(values, "")
	}
	assertNoCanary(t, all)
}

func assertResponse(
	t *testing.T,
	response capturedResponse,
	wantStatus int,
	wantContentType string,
	wantBody string,
) {
	t.Helper()
	if response.status != wantStatus {
		t.Fatalf("status = %d, want %d; body=%q", response.status, wantStatus, response.body)
	}
	if got := response.header.Get("Content-Type"); got != wantContentType {
		t.Fatalf("Content-Type = %q, want %q", got, wantContentType)
	}
	if response.body != wantBody {
		t.Fatalf("body = %q, want %q", response.body, wantBody)
	}
}

func gatewayConfigForAvailablePort(t *testing.T) gatewayConfig {
	t.Helper()
	probe, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("reserve loopback port: %v", err)
	}
	gatewayPort := probe.Addr().(*net.TCPAddr).Port
	probe.Close()
	if gatewayPort < 2 {
		t.Fatalf("unexpected gateway port %d", gatewayPort)
	}
	raw := gatewayYAMLForPorts(t, gatewayPort-1, gatewayPort)
	return readValidGatewayConfig(t, raw)
}

func gatewayYAMLForPorts(t *testing.T, converterPort, gatewayPort int) string {
	t.Helper()
	raw := strings.Replace(validGatewayYAML, "  port: 9716\n", "  port: "+strconv.Itoa(converterPort)+"\n", 1)
	raw = strings.ReplaceAll(raw, "127.0.0.1:9717", "127.0.0.1:"+strconv.Itoa(gatewayPort))
	raw = replaceExactlyOnce(
		t,
		raw,
		"  listen_port: 9717\n",
		"  listen_port: "+strconv.Itoa(gatewayPort)+"\n",
	)
	return raw
}

func readValidGatewayConfig(t *testing.T, raw string) gatewayConfig {
	t.Helper()
	digest := testDigest([]byte(raw))
	config, err := readGatewayConfig("ignored", digest, func(string) ([]byte, error) {
		return []byte(raw), nil
	})
	if err != nil {
		t.Fatalf("valid gateway config rejected: %v", err)
	}
	return config
}

func assertGatewayConfigRejected(t *testing.T, raw string) {
	t.Helper()
	_, err := readGatewayConfig("ignored", testDigest([]byte(raw)), func(string) ([]byte, error) {
		return []byte(raw), nil
	})
	if err == nil {
		t.Fatal("invalid gateway config accepted")
	}
	assertSafeDiagnostic(t, err.Error())
}

func replaceURLBlock(t *testing.T, raw, replacement string) string {
	t.Helper()
	return replaceExactlyOnce(
		t,
		raw,
		"  urls:\n"+
			"    - 'https://one.invalid/sub?token="+configSecretCanary+"'\n"+
			"    - 'http://two.invalid/path#"+configSecretCanary+"'\n",
		replacement,
	)
}

func replaceSubscriptionUserAgent(t *testing.T, raw, value string) string {
	t.Helper()
	return replaceExactlyOnce(
		t,
		raw,
		"subscription:\n"+
			"  url: 'http://127.0.0.1:9717/v1/aggregate'\n"+
			"  user_agent: 'sing-box/1.11 "+configSecretCanary+"'\n",
		"subscription:\n"+
			"  url: 'http://127.0.0.1:9717/v1/aggregate'\n"+
			"  user_agent: "+strconv.Quote(value)+"\n",
	)
}

func replaceGatewayUserAgent(t *testing.T, raw, value string) string {
	t.Helper()
	return replaceExactlyOnce(
		t,
		raw,
		"  aggregate_timeout: 70\n"+
			"  user_agent: 'sing-box/1.11 "+configSecretCanary+"'\n",
		"  aggregate_timeout: 70\n"+
			"  user_agent: "+strconv.Quote(value)+"\n",
	)
}

func replaceExactlyOnce(t *testing.T, raw, old, replacement string) string {
	t.Helper()
	if strings.Count(raw, old) != 1 {
		t.Fatalf("fixture contains %q %d times, want once", old, strings.Count(raw, old))
	}
	return strings.Replace(raw, old, replacement, 1)
}

func writeConfigFile(t *testing.T, raw string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), configSecretCanary+".yaml")
	if err := os.WriteFile(path, []byte(raw), 0o600); err != nil {
		t.Fatalf("write config: %v", err)
	}
	return path
}

func testDigest(raw []byte) string {
	sum := sha256.Sum256(raw)
	return fmt.Sprintf("%x", sum[:])
}

func assertSafeDiagnostic(t *testing.T, value string) {
	t.Helper()
	assertNoCanary(t, value)
	assertNotContains(t, value, "https://")
	assertNotContains(t, value, "http://")
	assertNotContains(t, value, "password")
	assertNotContains(t, value, "token")
}

func assertNoCanary(t *testing.T, value string) {
	t.Helper()
	assertNotContains(t, value, configSecretCanary)
	assertNotContains(t, value, engineSecretCanary)
}

func assertNotContains(t *testing.T, value, forbidden string) {
	t.Helper()
	if strings.Contains(value, forbidden) {
		t.Fatalf("value contains forbidden %q: %q", forbidden, value)
	}
}
