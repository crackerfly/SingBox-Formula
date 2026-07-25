package cmd

import (
	"context"
	"errors"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"testing"
	"time"

	"github.com/haierkeys/singbox-subscribe-convert/global"
	"go.uber.org/zap"
)

func TestConfigWatcherInvokesReloadCallback(t *testing.T) {
	configPath := writeWatcherConfig(t, "initial")
	reloaded := make(chan struct{}, 1)

	cw, err := startConfigWatcher(
		context.Background(),
		configPath,
		func() error {
			reloaded <- struct{}{}
			return nil
		},
		zap.NewNop(),
	)
	if err != nil {
		t.Fatalf("startConfigWatcher() error = %v", err)
	}
	t.Cleanup(cw.Close)

	if err := os.WriteFile(configPath, []byte("changed"), 0o600); err != nil {
		t.Fatalf("write watched config: %v", err)
	}
	awaitRunSignal(t, reloaded, "reload callback after config write")
}

func TestConfigWatcherAtomicReplacementReloadsExactlyOnce(t *testing.T) {
	configPath := writeWatcherConfig(t, "initial")
	var reloads atomic.Int32
	reloaded := make(chan struct{}, 4)

	cw, err := startConfigWatcher(
		context.Background(),
		configPath,
		func() error {
			reloads.Add(1)
			reloaded <- struct{}{}
			return nil
		},
		zap.NewNop(),
	)
	if err != nil {
		t.Fatalf("startConfigWatcher() error = %v", err)
	}
	t.Cleanup(cw.Close)

	replacement := filepath.Join(filepath.Dir(configPath), ".config.yaml.new")
	if err := os.WriteFile(replacement, []byte("replacement"), 0o600); err != nil {
		t.Fatalf("write replacement config: %v", err)
	}
	if err := os.Rename(replacement, configPath); err != nil {
		t.Fatalf("atomically replace config: %v", err)
	}

	awaitRunSignal(t, reloaded, "reload callback after atomic replacement")
	quiet := time.NewTimer(4 * configWatcherDebounceInterval)
	defer quiet.Stop()
	select {
	case <-reloaded:
		t.Fatal("atomic replacement invoked reload callback more than once")
	case <-quiet.C:
	}
	if got := reloads.Load(); got != 1 {
		t.Fatalf("reload callback calls = %d, want 1", got)
	}
}

func TestConfigWatcherCloseIsIdempotentAndWaitsForCallback(t *testing.T) {
	configPath := writeWatcherConfig(t, "initial")
	callbackStarted := make(chan struct{})
	releaseCallback := make(chan struct{})
	var callbackOnce sync.Once

	cw, err := startConfigWatcher(
		context.Background(),
		configPath,
		func() error {
			callbackOnce.Do(func() { close(callbackStarted) })
			<-releaseCallback
			return nil
		},
		zap.NewNop(),
	)
	if err != nil {
		t.Fatalf("startConfigWatcher() error = %v", err)
	}

	if err := os.WriteFile(configPath, []byte("changed"), 0o600); err != nil {
		t.Fatalf("write watched config: %v", err)
	}
	awaitRunSignal(t, callbackStarted, "blocking reload callback start")

	closed := make(chan struct{})
	go func() {
		cw.Close()
		close(closed)
	}()
	select {
	case <-closed:
		t.Fatal("Close() returned while reload callback was still running")
	case <-time.After(25 * time.Millisecond):
	}

	close(releaseCallback)
	awaitRunSignal(t, closed, "ConfigWatcher.Close completion")

	closedAgain := make(chan struct{})
	go func() {
		cw.Close()
		close(closedAgain)
	}()
	awaitRunSignal(t, closedAgain, "second ConfigWatcher.Close completion")
}

func TestRunServerWithHooksPropagatesReplacementFailure(t *testing.T) {
	bindErr := errors.New("bind failed")
	candidate := &global.Config{Server: global.ServerConfig{Port: 9716}}
	const realpath = "/canonical/config.yaml"

	var loadCalls atomic.Int32
	load := func(path string) (*global.Config, string, error) {
		if path != "config.yaml" {
			t.Fatalf("load path = %q, want config.yaml", path)
		}
		loadCalls.Add(1)
		return candidate, realpath, nil
	}
	current := newSupervisorFakeServer(nil)
	var factoryCalls atomic.Int32
	factory := func(cfg *global.Config, gotRealpath string) (serverInstance, error) {
		if cfg != candidate {
			t.Fatalf("factory config = %p, want explicit candidate %p", cfg, candidate)
		}
		if gotRealpath != realpath {
			t.Fatalf("factory realpath = %q, want %q", gotRealpath, realpath)
		}
		if factoryCalls.Add(1) == 1 {
			return current, nil
		}
		return nil, bindErr
	}

	reloadReady := make(chan func() error, 1)
	watcher := &runTestWatcher{}
	var signalChannel chan<- os.Signal
	var signalStopped atomic.Bool
	hooks := runServerHooks{
		load:      load,
		newServer: factory,
		logger:    func() *zap.Logger { return zap.NewNop() },
		startConfigWatcher: func(_ context.Context, gotPath string, reload func() error, _ *zap.Logger) (configWatcherCloser, error) {
			if gotPath != realpath {
				t.Fatalf("watcher path = %q, want canonical %q", gotPath, realpath)
			}
			reloadReady <- reload
			return watcher, nil
		},
		notifySignals: func(ch chan<- os.Signal) { signalChannel = ch },
		stopSignals: func(ch chan<- os.Signal) {
			if ch != signalChannel {
				t.Error("signal stop channel differs from notified channel")
			}
			signalStopped.Store(true)
		},
	}

	result := make(chan error, 1)
	go func() {
		result <- runServerWithHooks("config.yaml", realpath, hooks)
	}()

	var reload func() error
	select {
	case reload = <-reloadReady:
	case <-time.After(time.Second):
		t.Fatal("config watcher did not receive supervisor reload callback")
	}
	if err := reload(); !errors.Is(err, bindErr) {
		t.Fatalf("reload callback error = %v, want %v", err, bindErr)
	}

	select {
	case err := <-result:
		if !errors.Is(err, bindErr) {
			t.Fatalf("runServerWithHooks() error = %v, want %v", err, bindErr)
		}
	case <-time.After(time.Second):
		t.Fatal("runServerWithHooks() did not return after supervisor became terminal")
	}
	if got := loadCalls.Load(); got != 2 {
		t.Fatalf("candidate load calls = %d, want initial + reload", got)
	}
	if got := factoryCalls.Load(); got != 2 {
		t.Fatalf("factory calls = %d, want initial + failed replacement", got)
	}
	if got := current.shutdownCalls.Load(); got != 1 {
		t.Fatalf("current shutdown calls = %d, want 1", got)
	}
	if got := watcher.closeCalls.Load(); got != 1 {
		t.Fatalf("root watcher Close calls = %d, want 1", got)
	}
	if !signalStopped.Load() {
		t.Fatal("signal.Stop hook was not called")
	}
}

func TestRunServerWithHooksInvalidReloadKeepsCurrentRunning(t *testing.T) {
	invalidErr := errors.New("invalid candidate")
	candidate := &global.Config{Server: global.ServerConfig{Port: 9716}}
	var loadCalls atomic.Int32
	load := func(string) (*global.Config, string, error) {
		if loadCalls.Add(1) == 1 {
			return candidate, "/canonical/config.yaml", nil
		}
		return nil, "", invalidErr
	}
	current := newSupervisorFakeServer(nil)
	reloadReady := make(chan func() error, 1)
	var signalChannel chan<- os.Signal
	hooks := runServerHooks{
		load: load,
		newServer: func(*global.Config, string) (serverInstance, error) {
			return current, nil
		},
		logger: func() *zap.Logger { return zap.NewNop() },
		startConfigWatcher: func(_ context.Context, _ string, reload func() error, _ *zap.Logger) (configWatcherCloser, error) {
			reloadReady <- reload
			return &runTestWatcher{}, nil
		},
		notifySignals: func(ch chan<- os.Signal) { signalChannel = ch },
		stopSignals:   func(chan<- os.Signal) {},
	}

	result := make(chan error, 1)
	go func() { result <- runServerWithHooks("config.yaml", "/canonical/config.yaml", hooks) }()
	reload := awaitReloadCallback(t, reloadReady)
	if err := reload(); !errors.Is(err, invalidErr) {
		t.Fatalf("invalid reload error = %v, want %v", err, invalidErr)
	}
	if got := current.shutdownCalls.Load(); got != 0 {
		t.Fatalf("current shutdown calls after invalid reload = %d, want 0", got)
	}
	select {
	case err := <-result:
		t.Fatalf("root loop returned for invalid reload: %v", err)
	case <-time.After(25 * time.Millisecond):
	}

	signalChannel <- syscall.SIGTERM
	if err := awaitRunResult(t, result); err != nil {
		t.Fatalf("runServerWithHooks() signal shutdown error = %v", err)
	}
}

func TestRunServerWithHooksPropagatesCurrentServeFailure(t *testing.T) {
	serveErr := errors.New("serve failed")
	current := newSupervisorFakeServer(nil)
	watcherReady := make(chan struct{}, 1)
	hooks := runServerHooks{
		load: func(string) (*global.Config, string, error) {
			return &global.Config{Server: global.ServerConfig{Port: 9716}}, "/canonical/config.yaml", nil
		},
		newServer: func(*global.Config, string) (serverInstance, error) {
			return current, nil
		},
		logger: func() *zap.Logger { return zap.NewNop() },
		startConfigWatcher: func(context.Context, string, func() error, *zap.Logger) (configWatcherCloser, error) {
			watcherReady <- struct{}{}
			return &runTestWatcher{}, nil
		},
		notifySignals: func(chan<- os.Signal) {},
		stopSignals:   func(chan<- os.Signal) {},
	}

	result := make(chan error, 1)
	go func() { result <- runServerWithHooks("config.yaml", "/canonical/config.yaml", hooks) }()
	awaitRunSignal(t, watcherReady, "root watcher startup")
	current.doneErr = serveErr
	if err := current.Shutdown(); err != nil {
		t.Fatalf("publish fake serve failure: %v", err)
	}
	if err := awaitRunResult(t, result); !errors.Is(err, serveErr) {
		t.Fatalf("runServerWithHooks() error = %v, want %v", err, serveErr)
	}
}

func TestRunServerWithHooksSignalDoesNotHideConcurrentTerminalFailure(t *testing.T) {
	serveErr := errors.New("serve failed during signal cleanup")
	current := newSupervisorFakeServer(nil)
	watcherReady := make(chan struct{}, 1)
	var signalChannel chan<- os.Signal

	hooks := runServerHooks{
		load: func(string) (*global.Config, string, error) {
			return &global.Config{Server: global.ServerConfig{Port: 9716}}, "/canonical/config.yaml", nil
		},
		newServer: func(*global.Config, string) (serverInstance, error) {
			return current, nil
		},
		logger: func() *zap.Logger { return zap.NewNop() },
		startConfigWatcher: func(context.Context, string, func() error, *zap.Logger) (configWatcherCloser, error) {
			watcherReady <- struct{}{}
			return configWatcherCloseFunc(func() {
				current.doneErr = serveErr
				_ = current.Shutdown()
			}), nil
		},
		notifySignals: func(ch chan<- os.Signal) { signalChannel = ch },
		stopSignals:   func(chan<- os.Signal) {},
	}

	result := make(chan error, 1)
	go func() { result <- runServerWithHooks("config.yaml", "/canonical/config.yaml", hooks) }()
	awaitRunSignal(t, watcherReady, "root watcher startup")

	// The signal deterministically selects the signal branch. Closing the root
	// watcher then publishes a fatal current-server result before supervisor
	// shutdown completes. The fatal result must win over a clean signal exit.
	signalChannel <- syscall.SIGTERM
	if err := awaitRunResult(t, result); !errors.Is(err, serveErr) {
		t.Fatalf("runServerWithHooks() error = %v, want concurrent terminal %v", err, serveErr)
	}
}

func TestRunServerWithHooksWatcherStartFailureIsNonFatal(t *testing.T) {
	watchErr := errors.New("watch unavailable")
	current := newSupervisorFakeServer(nil)
	serverStarted := make(chan struct{}, 1)
	var signalChannel chan<- os.Signal
	var watcherStarts atomic.Int32
	hooks := runServerHooks{
		load: func(string) (*global.Config, string, error) {
			return &global.Config{Server: global.ServerConfig{Port: 9716}}, "/canonical/config.yaml", nil
		},
		newServer: func(*global.Config, string) (serverInstance, error) {
			serverStarted <- struct{}{}
			return current, nil
		},
		logger: func() *zap.Logger { return zap.NewNop() },
		startConfigWatcher: func(context.Context, string, func() error, *zap.Logger) (configWatcherCloser, error) {
			watcherStarts.Add(1)
			return nil, watchErr
		},
		notifySignals: func(ch chan<- os.Signal) { signalChannel = ch },
		stopSignals:   func(chan<- os.Signal) {},
	}

	result := make(chan error, 1)
	go func() { result <- runServerWithHooks("config.yaml", "/canonical/config.yaml", hooks) }()
	awaitRunSignal(t, serverStarted, "initial server startup")
	select {
	case err := <-result:
		t.Fatalf("watcher start failure stopped root server: %v", err)
	case <-time.After(25 * time.Millisecond):
	}
	signalChannel <- syscall.SIGTERM
	if err := awaitRunResult(t, result); err != nil {
		t.Fatalf("runServerWithHooks() after watcher failure = %v", err)
	}
	if got := watcherStarts.Load(); got != 1 {
		t.Fatalf("root watcher start calls = %d, want exactly 1", got)
	}
}

func TestRunServerWithHooksReloadsRealServerOnSamePortThreeTimes(t *testing.T) {
	preserveLifecycleGlobals(t)

	probe, err := net.Listen("tcp", ":0")
	if err != nil {
		t.Fatalf("listen on ephemeral port: %v", err)
	}
	port := probe.Addr().(*net.TCPAddr).Port
	if err := probe.Close(); err != nil {
		t.Fatalf("close ephemeral port probe: %v", err)
	}
	address := fmt.Sprintf(":%d", port)
	candidate := lifecycleTestConfig(t)
	candidate.Server.Port = port

	var factoryCalls atomic.Int32
	factory := func(cfg *global.Config, realpath string) (serverInstance, error) {
		runtime := lifecycleNoopRuntime()
		runtime.listen = ListenFunc(func(network, gotAddress string) (net.Listener, error) {
			if network != "tcp" || gotAddress != address {
				return nil, fmt.Errorf("listen = %q %q, want tcp %q", network, gotAddress, address)
			}
			return net.Listen(network, gotAddress)
		})
		server, err := newServerFromConfig(cfg, realpath, runtime)
		if err == nil {
			factoryCalls.Add(1)
		}
		return server, err
	}

	reloadReady := make(chan func() error, 1)
	var signalChannel chan<- os.Signal
	hooks := runServerHooks{
		load: func(string) (*global.Config, string, error) {
			return candidate, "/canonical/config.yaml", nil
		},
		newServer: factory,
		logger:    func() *zap.Logger { return zap.NewNop() },
		startConfigWatcher: func(_ context.Context, _ string, reload func() error, _ *zap.Logger) (configWatcherCloser, error) {
			reloadReady <- reload
			return &runTestWatcher{}, nil
		},
		notifySignals: func(ch chan<- os.Signal) { signalChannel = ch },
		stopSignals:   func(chan<- os.Signal) {},
	}

	result := make(chan error, 1)
	go func() { result <- runServerWithHooks("config.yaml", "/canonical/config.yaml", hooks) }()
	reload := awaitReloadCallback(t, reloadReady)
	for attempt := 1; attempt <= 3; attempt++ {
		if err := reload(); err != nil {
			t.Fatalf("same-port reload #%d error = %v", attempt, err)
		}
	}
	signalChannel <- syscall.SIGTERM
	if err := awaitRunResult(t, result); err != nil {
		t.Fatalf("runServerWithHooks() shutdown error = %v", err)
	}
	if got := factoryCalls.Load(); got != 4 {
		t.Fatalf("real server factory calls = %d, want initial + 3 reloads", got)
	}
}

func TestRunLifecycleContainsNoFixedSleepOrLegacyCloseShim(t *testing.T) {
	for _, filename := range []string{"run.go", "run_server.go"} {
		source, err := os.ReadFile(filename)
		if err != nil {
			t.Fatalf("read %s: %v", filename, err)
		}
		if strings.Contains(string(source), "time.Sleep(") {
			t.Fatalf("%s still contains a fixed time.Sleep lifecycle delay", filename)
		}
		if strings.Contains(string(source), "serverCloseSignal") {
			t.Fatalf("%s still contains the legacy serverCloseSignal shim", filename)
		}
	}
}

func writeWatcherConfig(t *testing.T, contents string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "config.yaml")
	if err := os.WriteFile(path, []byte(contents), 0o600); err != nil {
		t.Fatalf("write initial config: %v", err)
	}
	return path
}

func awaitRunSignal(t *testing.T, ch <-chan struct{}, description string) {
	t.Helper()
	select {
	case <-ch:
	case <-time.After(2 * time.Second):
		t.Fatalf("timed out waiting for %s", description)
	}
}

func awaitReloadCallback(t *testing.T, callbacks <-chan func() error) func() error {
	t.Helper()
	select {
	case callback := <-callbacks:
		return callback
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for config watcher reload callback")
		return nil
	}
}

func awaitRunResult(t *testing.T, result <-chan error) error {
	t.Helper()
	select {
	case err := <-result:
		return err
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for root server loop result")
		return nil
	}
}

type runTestWatcher struct {
	closeCalls atomic.Int32
}

func (w *runTestWatcher) Close() {
	w.closeCalls.Add(1)
}

type configWatcherCloseFunc func()

func (closeFn configWatcherCloseFunc) Close() {
	closeFn()
}
