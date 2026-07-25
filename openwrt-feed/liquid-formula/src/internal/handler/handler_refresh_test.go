package handler

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/haierkeys/singbox-subscribe-convert/global"
	"github.com/haierkeys/singbox-subscribe-convert/internal/cache"
	"go.uber.org/zap"
)

func TestManualRefreshDoesNotPreDeleteAndFailurePreservesLastKnownGood(t *testing.T) {
	config := handlerTestConfig(t)
	writeHandlerGeneration(t, config, "old")
	if err := Init(config, zap.NewNop()); err != nil {
		t.Fatalf("Init() error = %v", err)
	}

	fetchStarted := make(chan struct{})
	releaseFetch := make(chan struct{})
	restore := replaceFetchRemoteForTest(func(context.Context, string, int64) ([]byte, string, error) {
		close(fetchStarted)
		<-releaseFetch
		return nil, "https://example.test/full?token=secret&_t=1&_r=2", errors.New("download failed")
	})
	t.Cleanup(restore)

	request := httptest.NewRequest(http.MethodPost, "/refresh?password=890716", nil)
	response := httptest.NewRecorder()
	done := make(chan struct{})
	go func() {
		HandleRefresh(response, request)
		close(done)
	}()
	<-fetchStarted

	if got := readHandlerFile(t, config.GetNodeFilePath()); !strings.Contains(got, "old") {
		t.Fatalf("node cache changed before fetch completed: %q", got)
	}
	if got := readHandlerFile(t, config.GetTemplateFilePathByName("default")); !strings.Contains(got, "old") {
		t.Fatalf("template cache changed before fetch completed: %q", got)
	}
	close(releaseFetch)
	<-done

	if response.Code != http.StatusInternalServerError {
		t.Fatalf("manual status = %d, want %d", response.Code, http.StatusInternalServerError)
	}
	assertHandlerGeneration(t, config, "old")
	assertRenderedGeneration(t, config, "old")
}

func TestInitLoadFailureStartsWithEmptySnapshotInsteadOfPreviousServerGeneration(t *testing.T) {
	oldConfig := handlerTestConfig(t)
	writeHandlerGeneration(t, oldConfig, "old-server")
	if err := Init(oldConfig, zap.NewNop()); err != nil {
		t.Fatalf("Init(old) error = %v", err)
	}
	if got := getSnapshot(); len(got.nodes) == 0 || len(got.templates) == 0 {
		t.Fatal("old server snapshot did not load")
	}

	newConfig := handlerTestConfig(t)
	if err := Init(newConfig, zap.NewNop()); err != nil {
		t.Fatalf("Init(new) error = %v", err)
	}
	if got := getSnapshot(); len(got.nodes) != 0 || len(got.nodeData) != 0 || len(got.templates) != 0 {
		t.Fatalf("Init load failure retained previous server snapshot: nodes=%d data=%d templates=%d", len(got.nodes), len(got.nodeData), len(got.templates))
	}
}

func TestBuildSnapshotRejectsOutboundsWithoutNonEmptyStringTag(t *testing.T) {
	config := handlerTestConfig(t)
	writeHandlerGeneration(t, config, "old")
	if err := Init(config, zap.NewNop()); err != nil {
		t.Fatalf("Init() error = %v", err)
	}
	invalidNodes := []byte(`{"outbounds":[{"tag":""},{"tag":7},{"type":"direct"}]}`)
	if _, err := buildSnapshot(invalidNodes, map[string][]byte{"default": handlerTemplate("new")}); err == nil {
		t.Fatal("buildSnapshot() error = nil, want no valid node tags error")
	}
}

func TestRefreshValidationFailurePreservesDiskAndMemory(t *testing.T) {
	config := handlerTestConfig(t)
	writeHandlerGeneration(t, config, "old")
	if err := Init(config, zap.NewNop()); err != nil {
		t.Fatalf("Init() error = %v", err)
	}
	restore := replaceFetchRemoteForTest(func(_ context.Context, rawURL string, _ int64) ([]byte, string, error) {
		if strings.Contains(rawURL, "nodes") {
			return []byte(`{"outbounds":`), rawURL + "?_t=1&_r=2", nil
		}
		return handlerTemplate("new"), rawURL + "?_t=1&_r=2", nil
	})
	t.Cleanup(restore)

	if _, err := Refresh(context.Background(), "manual"); err == nil {
		t.Fatal("Refresh() error = nil, want node validation failure")
	}
	assertHandlerGeneration(t, config, "old")
	assertRenderedGeneration(t, config, "old")
}

func TestRefreshCommitsBeforeSingleSnapshotApply(t *testing.T) {
	config := handlerTestConfig(t)
	writeHandlerGeneration(t, config, "old")
	if err := Init(config, zap.NewNop()); err != nil {
		t.Fatalf("Init() error = %v", err)
	}
	restoreFetch := replaceFetchRemoteForTest(func(_ context.Context, rawURL string, _ int64) ([]byte, string, error) {
		if strings.Contains(rawURL, "nodes") {
			return handlerNodes("new"), rawURL + "?_t=1&_r=2", nil
		}
		return handlerTemplate("new"), rawURL + "?_t=1&_r=2", nil
	})
	t.Cleanup(restoreFetch)

	commitObservedOldMemory := false
	restoreCommit := replaceCommitBatchForTest(func(commit func() error) error {
		assertRenderedGeneration(t, config, "old")
		commitObservedOldMemory = true
		return commit()
	})
	t.Cleanup(restoreCommit)

	result, err := Refresh(context.Background(), "auto")
	if err != nil {
		t.Fatalf("Refresh() error = %v", err)
	}
	if got := result.URLs["node"]; !strings.Contains(got, "token=node-secret") || !strings.Contains(got, "_t=1&_r=2") {
		t.Fatalf("Refresh() node URL = %q, want full cache-busted URL", got)
	}
	if !commitObservedOldMemory {
		t.Fatal("commit seam was not called")
	}
	assertHandlerGeneration(t, config, "new")
	assertRenderedGeneration(t, config, "new")
}

func TestRefreshCommitFailurePreservesDiskAndMemory(t *testing.T) {
	config := handlerTestConfig(t)
	writeHandlerGeneration(t, config, "old")
	if err := Init(config, zap.NewNop()); err != nil {
		t.Fatalf("Init() error = %v", err)
	}
	restoreFetch := replaceFetchRemoteForTest(func(_ context.Context, rawURL string, _ int64) ([]byte, string, error) {
		if strings.Contains(rawURL, "nodes") {
			return handlerNodes("new"), rawURL + "?_t=1&_r=2", nil
		}
		return handlerTemplate("new"), rawURL + "?_t=1&_r=2", nil
	})
	t.Cleanup(restoreFetch)
	injected := errors.New("commit failed")
	restoreCommit := replaceCommitBatchForTest(func(func() error) error { return injected })
	t.Cleanup(restoreCommit)

	if _, err := Refresh(context.Background(), TriggerManual); !errors.Is(err, injected) {
		t.Fatalf("Refresh() error = %v, want %v", err, injected)
	}
	assertHandlerGeneration(t, config, "old")
	assertRenderedGeneration(t, config, "old")
}

func TestOnDemandTemplateFailureRetainsLoadedLastKnownGood(t *testing.T) {
	config := handlerTestConfig(t)
	config.Templates["default"] = global.TemplateConfig{
		URL: "https://example.test/template?token=template-secret", Name: "default", NoNode: "DIRECT", Enabled: true, UpdateInterval: 1,
	}
	writeHandlerGeneration(t, config, "old")
	old := time.Now().Add(-time.Hour)
	if err := os.Chtimes(config.GetTemplateFilePathByName("default"), old, old); err != nil {
		t.Fatal(err)
	}
	if err := Init(config, zap.NewNop()); err != nil {
		t.Fatalf("Init() error = %v", err)
	}
	restore := replaceFetchRemoteForTest(func(context.Context, string, int64) ([]byte, string, error) {
		return nil, "https://example.test/template?token=template-secret&_t=1&_r=2", errors.New("offline")
	})
	t.Cleanup(restore)

	if err := EnsureTemplate("default"); err != nil {
		t.Fatalf("EnsureTemplate() error = %v, want loaded last-known-good fallback", err)
	}
	assertHandlerGeneration(t, config, "old")
	assertRenderedGeneration(t, config, "old")
}

func TestQueryRefreshFailureServesLastKnownGood(t *testing.T) {
	config := handlerTestConfig(t)
	writeHandlerGeneration(t, config, "old")
	if err := Init(config, zap.NewNop()); err != nil {
		t.Fatalf("Init() error = %v", err)
	}
	restore := replaceFetchRemoteForTest(func(context.Context, string, int64) ([]byte, string, error) {
		return nil, "https://example.test/full?token=secret&_t=1&_r=2", errors.New("offline")
	})
	t.Cleanup(restore)

	response := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodGet, "/?password=890716&refresh=1", nil)
	HandleRequest(response, request)
	if response.Code != http.StatusOK {
		t.Fatalf("query refresh status = %d, want %d; body=%s", response.Code, http.StatusOK, response.Body.String())
	}
	if !strings.Contains(response.Body.String(), "old") {
		t.Fatalf("query refresh body = %q, want last-known-good", response.Body.String())
	}
}

func TestRequestUsesOneImmutableSnapshot(t *testing.T) {
	config := handlerTestConfig(t)
	writeHandlerGeneration(t, config, "old")
	if err := Init(config, zap.NewNop()); err != nil {
		t.Fatalf("Init() error = %v", err)
	}
	restoreHook := replaceRequestSnapshotHookForTest(func() {
		candidate, err := buildSnapshot(handlerNodes("new"), map[string][]byte{"default": handlerTemplate("new")})
		if err != nil {
			t.Fatalf("buildSnapshot(new) error = %v", err)
		}
		applySnapshot(candidate)
	})
	t.Cleanup(restoreHook)

	response := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodGet, "/?password=890716", nil)
	HandleRequest(response, request)
	if response.Code != http.StatusOK {
		t.Fatalf("request status = %d, want %d", response.Code, http.StatusOK)
	}
	if strings.Contains(response.Body.String(), "new") || !strings.Contains(response.Body.String(), "old") {
		t.Fatalf("request mixed snapshots: %q", response.Body.String())
	}
}

func TestConcurrentRefreshEntryPointsShareHandlerManager(t *testing.T) {
	config := handlerTestConfig(t)
	writeHandlerGeneration(t, config, "old")
	if err := Init(config, zap.NewNop()); err != nil {
		t.Fatalf("Init() error = %v", err)
	}
	entered := make(chan struct{}, 9)
	release := make(chan struct{})
	var active atomic.Int32
	var maximum atomic.Int32
	restore := replaceFetchRemoteForTest(func(_ context.Context, rawURL string, _ int64) ([]byte, string, error) {
		current := active.Add(1)
		for {
			old := maximum.Load()
			if current <= old || maximum.CompareAndSwap(old, current) {
				break
			}
		}
		entered <- struct{}{}
		<-release
		active.Add(-1)
		if strings.Contains(rawURL, "nodes") {
			return handlerNodes("new"), rawURL + "&_t=1&_r=2", nil
		}
		return handlerTemplate("new"), rawURL + "&_t=1&_r=2", nil
	})
	t.Cleanup(restore)

	operations := []func() error{
		func() error { _, err := Refresh(context.Background(), TriggerInitial); return err },
		func() error { _, err := Refresh(context.Background(), TriggerManual); return err },
		func() error { _, err := Refresh(context.Background(), TriggerQuery); return err },
		func() error { _, err := Refresh(context.Background(), TriggerAuto); return err },
		func() error { return refreshTemplate(context.Background(), "default") },
	}
	errorsSeen := make(chan error, len(operations))
	var workers sync.WaitGroup
	for _, operation := range operations {
		operation := operation
		workers.Add(1)
		go func() {
			defer workers.Done()
			errorsSeen <- operation()
		}()
	}
	for i := 0; i < 9; i++ {
		select {
		case <-entered:
		case <-time.After(time.Second):
			t.Fatalf("timed out waiting for fetch %d", i+1)
		}
		select {
		case <-entered:
			t.Fatal("a second refresh fetch entered while the handler permit was held")
		case <-time.After(10 * time.Millisecond):
		}
		release <- struct{}{}
	}
	workers.Wait()
	close(errorsSeen)
	for err := range errorsSeen {
		if err != nil {
			t.Fatalf("refresh entry point error = %v", err)
		}
	}
	if got := maximum.Load(); got != 1 {
		t.Fatalf("maximum active handler refreshes = %d, want 1", got)
	}
}

func handlerTestConfig(t *testing.T) *global.Config {
	t.Helper()
	return &global.Config{
		Server: global.ServerConfig{Port: 9716, ReadTimeout: 1, WriteTimeout: 2, IdleTimeout: 1},
		Auth:   global.AuthConfig{Password: "890716"},
		Subscription: global.SubscriptionConfig{
			URL: "https://example.test/nodes?token=node-secret", Timeout: 1, RefreshInterval: 1,
		},
		Templates: map[string]global.TemplateConfig{
			"default": {URL: "https://example.test/template?token=template-secret", Name: "default", NoNode: "DIRECT", Enabled: true},
		},
		DefaultTemplate: "default",
		Cache:           global.CacheConfig{Directory: t.TempDir(), NodeFile: "nodes.json"},
	}
}

func writeHandlerGeneration(t *testing.T, config *global.Config, generation string) {
	t.Helper()
	if err := os.MkdirAll(config.Cache.Directory, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(config.GetNodeFilePath(), handlerNodes(generation), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(config.GetTemplateFilePathByName("default"), handlerTemplate(generation), 0o600); err != nil {
		t.Fatal(err)
	}
}

func handlerNodes(generation string) []byte {
	return []byte(`{"outbounds":[{"tag":"` + generation + `","type":"direct"}]}`)
}

func handlerTemplate(generation string) []byte {
	return []byte(`{"generation":"` + generation + `","nodes":[{{ Nodes }}],"count":{{ nodeCount }}}`)
}

func assertHandlerGeneration(t *testing.T, config *global.Config, generation string) {
	t.Helper()
	for _, path := range []string{config.GetNodeFilePath(), config.GetTemplateFilePathByName("default")} {
		if got := readHandlerFile(t, path); !strings.Contains(got, generation) {
			t.Fatalf("%s = %q, want generation %q", filepath.Base(path), got, generation)
		}
	}
}

func assertRenderedGeneration(t *testing.T, config *global.Config, generation string) {
	t.Helper()
	response := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodGet, "/?password="+config.Auth.Password, nil)
	HandleRequest(response, request)
	if response.Code != http.StatusOK || !strings.Contains(response.Body.String(), generation) {
		t.Fatalf("render status/body = %d %q, want generation %q", response.Code, response.Body.String(), generation)
	}
}

func readHandlerFile(t *testing.T, path string) string {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	return string(data)
}

func replaceFetchRemoteForTest(replacement func(context.Context, string, int64) ([]byte, string, error)) func() {
	previous := fetchRemoteFn
	fetchRemoteFn = replacement
	return func() { fetchRemoteFn = previous }
}

func replaceCommitBatchForTest(replacement func(func() error) error) func() {
	previous := commitBatchFn
	commitBatchFn = func(batch *cache.Batch) error { return replacement(batch.Commit) }
	return func() { commitBatchFn = previous }
}

func replaceRequestSnapshotHookForTest(replacement func()) func() {
	previous := requestSnapshotHook
	requestSnapshotHook = replacement
	return func() { requestSnapshotHook = previous }
}
