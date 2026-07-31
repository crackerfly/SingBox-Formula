package main

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestProductionServeDependenciesFetchNormalizeAndCommitToDisk(
	t *testing.T,
) {
	const userAgent = "liquid-formula-production-wiring-test"
	source := httptest.NewServer(http.HandlerFunc(
		func(writer http.ResponseWriter, request *http.Request) {
			if request.UserAgent() != userAgent {
				t.Errorf(
					"source user agent = %q, want %q",
					request.UserAgent(), userAgent,
				)
			}
			writer.Header().Set("Content-Type", "application/json")
			_, _ = writer.Write([]byte(
				`{"outbounds":[{"type":"socks","tag":"Fetched","server":"127.0.0.1","server_port":1080}]}`,
			))
		},
	))
	defer source.Close()

	stateRoot := productionWiringStateRoot(t)
	runtime := subscriptionEngineRuntime{
		StateRoot: stateRoot,
		LockPath:  filepath.Join(t.TempDir(), "subscription.lock"),
		LockRetry: time.Millisecond,
	}
	config := productionWiringConfig(
		source.URL,
		filepath.Join(t.TempDir(), "node.json"),
		userAgent,
	)

	dependencies := newProductionServeDependencies(runtime)
	if dependencies.NewEngine == nil {
		t.Fatal("production serve dependencies have no engine factory")
	}
	outcome := dependencies.NewEngine(config).Aggregate(
		context.Background(),
	)
	if outcome.Code != "" || len(outcome.Bytes) == 0 {
		t.Fatalf("production aggregate outcome = %#v", outcome)
	}

	var aggregate struct {
		Outbounds []map[string]any `json:"outbounds"`
	}
	if err := json.Unmarshal(outcome.Bytes, &aggregate); err != nil {
		t.Fatalf("decode aggregate: %v", err)
	}
	if len(aggregate.Outbounds) != 1 ||
		aggregate.Outbounds[0]["tag"] != "Fetched" ||
		aggregate.Outbounds[0]["type"] != "socks" {
		t.Fatalf("aggregate outbounds = %#v", aggregate.Outbounds)
	}

	selection, err := newDiskGenerationStore(stateRoot).LoadCurrent(
		context.Background(),
	)
	if err != nil {
		t.Fatalf("load committed current: %v", err)
	}
	if selection.Kind != currentPresent ||
		selection.Generation.ConfigDigest != config.ConfigDigest ||
		len(selection.Generation.Sources) != 1 {
		t.Fatalf("committed selection = %#v", selection)
	}
}

func TestProductionServeDependenciesShareLegacyProviderWithDiskStore(
	t *testing.T,
) {
	source := httptest.NewServer(http.HandlerFunc(
		func(writer http.ResponseWriter, _ *http.Request) {
			writer.WriteHeader(http.StatusServiceUnavailable)
		},
	))
	defer source.Close()

	stateRoot := productionWiringStateRoot(t)
	cacheDirectory := t.TempDir()
	legacyNodePath := filepath.Join(cacheDirectory, "node.json")
	if err := os.WriteFile(
		legacyNodePath,
		[]byte("{\n  \"outbounds\": [\n    {\"type\":\"socks\",\"tag\":\"Legacy\",\"server\":\"127.0.0.1\",\"server_port\":1080}\n  ]\n}\n"),
		0644,
	); err != nil {
		t.Fatal(err)
	}
	urlDigest := productionWiringDigest(source.URL)
	markerPath := filepath.Join(stateRoot, legacyMarkerName)
	if err := os.WriteFile(
		markerPath,
		[]byte(urlDigest+"\n"),
		0600,
	); err != nil {
		t.Fatal(err)
	}

	config := productionWiringConfig(
		source.URL,
		legacyNodePath,
		"legacy-adoption-test",
	)
	dependencies := newProductionServeDependencies(
		subscriptionEngineRuntime{
			StateRoot: stateRoot,
			LockPath:  filepath.Join(t.TempDir(), "subscription.lock"),
			LockRetry: time.Millisecond,
		},
	)
	outcome := dependencies.NewEngine(config).Aggregate(
		context.Background(),
	)
	if outcome.Code != "" || len(outcome.Bytes) == 0 {
		t.Fatalf("legacy aggregate outcome = %#v", outcome)
	}

	selection, err := newDiskGenerationStore(stateRoot).LoadCurrent(
		context.Background(),
	)
	if err != nil {
		t.Fatalf("load legacy-backed current: %v", err)
	}
	if selection.Kind != currentPresent ||
		selection.Generation.LegacyConsumedURLDigest != urlDigest ||
		len(selection.Generation.Sources) != 1 ||
		selection.Generation.Sources[0].Info.Format !=
			FormatSingBoxJSON {
		t.Fatalf("legacy-backed selection = %#v", selection)
	}
	if _, err := os.Lstat(markerPath); !os.IsNotExist(err) {
		t.Fatalf("committed legacy marker still exists: %v", err)
	}
}

func TestProductionServeDependenciesInitializePristineStateRoot(
	t *testing.T,
) {
	source := httptest.NewServer(http.HandlerFunc(
		func(writer http.ResponseWriter, _ *http.Request) {
			_, _ = writer.Write([]byte(
				`{"outbounds":[{"type":"socks","tag":"First install","server":"127.0.0.1","server_port":1080}]}`,
			))
		},
	))
	defer source.Close()

	stateParent := t.TempDir()
	stateRoot := filepath.Join(stateParent, "subscriptions")
	config := productionWiringConfig(
		source.URL,
		filepath.Join(t.TempDir(), "node.json"),
		"first-install-test",
	)
	dependencies := newProductionServeDependencies(
		subscriptionEngineRuntime{
			StateRoot: stateRoot,
			LockPath:  filepath.Join(t.TempDir(), "subscription.lock"),
			LockRetry: time.Millisecond,
		},
	)
	outcome := dependencies.NewEngine(config).Aggregate(
		context.Background(),
	)
	if outcome.Code != "" || len(outcome.Bytes) == 0 {
		t.Fatalf("first-install aggregate outcome = %#v", outcome)
	}
	for _, path := range []string{
		stateRoot,
		filepath.Join(stateRoot, "generations"),
		filepath.Join(stateRoot, "objects"),
	} {
		info, err := os.Lstat(path)
		if err != nil {
			t.Fatalf("stat initialized state %q: %v", path, err)
		}
		if !info.IsDir() || info.Mode().Perm() != 0700 {
			t.Fatalf(
				"initialized state %q mode = %v",
				path, info.Mode(),
			)
		}
	}
}

func TestProductionServeDependenciesRejectStateInitializationFailure(
	t *testing.T,
) {
	stateRoot := filepath.Join(t.TempDir(), "subscriptions")
	if err := os.WriteFile(stateRoot, []byte("not a directory"), 0600); err != nil {
		t.Fatal(err)
	}
	config := productionWiringConfig(
		"https://source.invalid/subscription",
		filepath.Join(t.TempDir(), "node.json"),
		"invalid-state-test",
	)
	dependencies := newProductionServeDependencies(
		subscriptionEngineRuntime{
			StateRoot: stateRoot,
			LockPath:  filepath.Join(t.TempDir(), "subscription.lock"),
			LockRetry: time.Millisecond,
		},
	)
	if engine := dependencies.NewEngine(config); engine != nil {
		t.Fatalf(
			"state initialization failure returned ready engine %T",
			engine,
		)
	}
}

func TestGatewayConfigDerivesLegacyNodeFromValidatedCacheDirectory(
	t *testing.T,
) {
	cacheDirectory := filepath.Join(t.TempDir(), "custom-cache")
	raw := productionWiringConfigYAML(
		"cache:\n" +
			"  directory: '" + cacheDirectory + "'\n" +
			"  node_file: 'node.json'\n",
	)
	config, err := readGatewayConfig(
		"/config.yaml",
		productionWiringDigest(raw),
		func(string) ([]byte, error) {
			return []byte(raw), nil
		},
	)
	if err != nil {
		t.Fatalf("readGatewayConfig: %v", err)
	}
	want := filepath.Join(cacheDirectory, "node.json")
	if config.LegacyNodePath != want {
		t.Fatalf(
			"legacy node path = %q, want %q",
			config.LegacyNodePath, want,
		)
	}
}

func TestGatewayConfigUsesExistingDefaultLegacyNodePath(
	t *testing.T,
) {
	config, err := readGatewayConfig(
		"/config.yaml",
		validGatewayDigest,
		func(string) ([]byte, error) {
			return []byte(validGatewayYAML), nil
		},
	)
	if err != nil {
		t.Fatalf("readGatewayConfig: %v", err)
	}
	const want = "/var/lib/liquid-formula/cache/node.json"
	if config.LegacyNodePath != want {
		t.Fatalf(
			"default legacy node path = %q, want %q",
			config.LegacyNodePath, want,
		)
	}
}

func TestGatewayConfigRejectsUnsafeLegacyCacheLocation(
	t *testing.T,
) {
	for name, cacheYAML := range map[string]string{
		"cache is not a mapping": "cache: 'not-a-mapping'\n",
		"relative directory": "cache:\n" +
			"  directory: 'relative/cache'\n" +
			"  node_file: 'node.json'\n",
		"directory is not a string": "cache:\n" +
			"  directory: 7\n" +
			"  node_file: 'node.json'\n",
		"directory contains a control character": "cache:\n" +
			"  directory: \"/tmp/cache\\nother\"\n" +
			"  node_file: 'node.json'\n",
		"directory contains DEL": "cache:\n" +
			"  directory: \"/tmp/cache\\x7fother\"\n" +
			"  node_file: 'node.json'\n",
		"missing directory": "cache:\n" +
			"  node_file: 'node.json'\n",
		"missing node file": "cache:\n" +
			"  directory: '/tmp/cache'\n",
		"alternate node file": "cache:\n" +
			"  directory: '/tmp/cache'\n" +
			"  node_file: 'other.json'\n",
	} {
		t.Run(name, func(t *testing.T) {
			raw := productionWiringConfigYAML(cacheYAML)
			if _, err := readGatewayConfig(
				"/config.yaml",
				productionWiringDigest(raw),
				func(string) ([]byte, error) {
					return []byte(raw), nil
				},
			); err == nil {
				t.Fatal("unsafe legacy cache location was accepted")
			}
		})
	}
}

func productionWiringConfigYAML(cacheYAML string) string {
	return strings.Replace(
		validGatewayYAML,
		"liquid_formula_gateway:\n",
		cacheYAML+"liquid_formula_gateway:\n",
		1,
	)
}

func productionWiringStateRoot(t *testing.T) string {
	t.Helper()
	root := filepath.Join(t.TempDir(), "subscriptions")
	if err := os.Mkdir(root, 0700); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"generations", "objects"} {
		if err := os.Mkdir(filepath.Join(root, name), 0700); err != nil {
			t.Fatal(err)
		}
	}
	return root
}

func productionWiringConfig(
	rawURL string,
	legacyNodePath string,
	userAgent string,
) gatewayConfig {
	return gatewayConfig{
		ConfigDigest:         productionWiringDigest("config"),
		SourceTimeoutSeconds: 5,
		UserAgent:            userAgent,
		LegacyNodePath:       legacyNodePath,
		Sources: []gatewaySource{{
			URL:       rawURL,
			URLDigest: productionWiringDigest(rawURL),
		}},
	}
}

func productionWiringDigest(value string) string {
	sum := sha256.Sum256([]byte(value))
	return hex.EncodeToString(sum[:])
}
