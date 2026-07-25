package cmd

import (
	"context"
	"errors"
	"io"
	"net"
	"net/http"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/haierkeys/singbox-subscribe-convert/global"
	"go.uber.org/zap"
)

func TestNewServerFromConfigListensOnExactAllInterfacesAddress(t *testing.T) {
	preserveLifecycleGlobals(t)

	listener := newLifecycleListener(nil)
	updater := newLifecycleWorker()
	cacheWatcher := newLifecycleWorker()
	var gotNetwork, gotAddress string
	runtime := lifecycleTestRuntime(listener, updater, cacheWatcher)
	runtime.listen = ListenFunc(func(network, address string) (net.Listener, error) {
		gotNetwork, gotAddress = network, address
		return listener, nil
	})

	server, err := newServerFromConfig(lifecycleTestConfig(t), "/real/config.yaml", runtime)
	if err != nil {
		t.Fatalf("newServerFromConfig() error = %v", err)
	}
	t.Cleanup(func() {
		updater.releaseNow()
		cacheWatcher.releaseNow()
		_ = server.Shutdown()
	})

	if gotNetwork != "tcp" {
		t.Fatalf("listen network = %q, want %q", gotNetwork, "tcp")
	}
	if gotAddress != ":9716" {
		t.Fatalf("listen address = %q, want exact all-interfaces address %q", gotAddress, ":9716")
	}
}

func TestNewServerFromConfigBindFailureStartsNoWorkersAndDoesNotCommitGlobals(t *testing.T) {
	previousCfg, previousPath, previousLogger := global.Cfg, global.ConfigFile, global.Logger
	originalCfg := &global.Config{Server: global.ServerConfig{Port: 8080}}
	originalPath := "/committed/config.yaml"
	originalLogger := zap.NewNop()
	global.Cfg, global.ConfigFile, global.Logger = originalCfg, originalPath, originalLogger
	t.Cleanup(func() { global.Cfg, global.ConfigFile, global.Logger = previousCfg, previousPath, previousLogger })

	bindErr := errors.New("bind failed")
	var updaterStarts, watcherStarts atomic.Int32
	runtime := lifecycleNoopRuntime()
	runtime.listen = ListenFunc(func(string, string) (net.Listener, error) {
		return nil, bindErr
	})
	runtime.runUpdater = func(context.Context, *global.Config) {
		updaterStarts.Add(1)
	}
	runtime.runWatcher = func(context.Context, *global.Config) {
		watcherStarts.Add(1)
	}

	server, err := newServerFromConfig(lifecycleTestConfig(t), "/candidate/config.yaml", runtime)
	if !errors.Is(err, bindErr) {
		t.Fatalf("newServerFromConfig() error = %v, want %v", err, bindErr)
	}
	if server != nil {
		t.Fatalf("newServerFromConfig() server = %#v, want nil after bind failure", server)
	}
	if got := updaterStarts.Load(); got != 0 {
		t.Fatalf("updater starts = %d, want 0", got)
	}
	if got := watcherStarts.Load(); got != 0 {
		t.Fatalf("watcher starts = %d, want 0", got)
	}
	if global.Cfg != originalCfg {
		t.Fatalf("global.Cfg changed on bind failure: got %p, want %p", global.Cfg, originalCfg)
	}
	if global.ConfigFile != originalPath {
		t.Fatalf("global.ConfigFile = %q, want unchanged %q", global.ConfigFile, originalPath)
	}
	if global.Logger != originalLogger {
		t.Fatalf("global.Logger changed on bind failure: got %p, want %p", global.Logger, originalLogger)
	}
}

func TestNewServerFromConfigInitializeFailureCleansUpWithoutCommittingGlobals(t *testing.T) {
	previousCfg, previousPath, previousLogger := global.Cfg, global.ConfigFile, global.Logger
	originalCfg := &global.Config{Server: global.ServerConfig{Port: 8080}}
	originalPath := "/committed/config.yaml"
	originalLogger := zap.NewNop()
	global.Cfg, global.ConfigFile, global.Logger = originalCfg, originalPath, originalLogger
	t.Cleanup(func() { global.Cfg, global.ConfigFile, global.Logger = previousCfg, previousPath, previousLogger })

	initializeErr := errors.New("initialize failed")
	listener := newLifecycleListener(nil)
	var updaterStarts, watcherStarts atomic.Int32
	runtime := lifecycleNoopRuntime()
	runtime.listen = ListenFunc(func(string, string) (net.Listener, error) {
		return listener, nil
	})
	runtime.initialize = func(server *Server, cfg *global.Config, _ string) error {
		if err := server.initLogger(cfg); err != nil {
			return err
		}
		return initializeErr
	}
	runtime.runUpdater = func(context.Context, *global.Config) {
		updaterStarts.Add(1)
	}
	runtime.runWatcher = func(context.Context, *global.Config) {
		watcherStarts.Add(1)
	}

	server, err := newServerFromConfig(lifecycleTestConfig(t), "/candidate/config.yaml", runtime)
	if !errors.Is(err, initializeErr) {
		t.Fatalf("newServerFromConfig() error = %v, want %v", err, initializeErr)
	}
	if server != nil {
		t.Fatalf("newServerFromConfig() server = %#v, want nil after initialize failure", server)
	}
	awaitLifecycleSignal(t, listener.closed, "listener close after initialize failure")
	if got := listener.accepts.Load(); got != 0 {
		t.Fatalf("listener Accept calls = %d, want 0 after initialize failure", got)
	}
	if got := updaterStarts.Load(); got != 0 {
		t.Fatalf("updater starts = %d, want 0", got)
	}
	if got := watcherStarts.Load(); got != 0 {
		t.Fatalf("watcher starts = %d, want 0", got)
	}
	if global.Cfg != originalCfg {
		t.Fatalf("global.Cfg changed on initialize failure: got %p, want %p", global.Cfg, originalCfg)
	}
	if global.ConfigFile != originalPath {
		t.Fatalf("global.ConfigFile = %q, want unchanged %q", global.ConfigFile, originalPath)
	}
	if global.Logger != originalLogger {
		t.Fatalf("global.Logger changed on initialize failure: got %p, want %p", global.Logger, originalLogger)
	}
}

func TestServerShutdownWaitsForUpdaterAndWatcherBeforeBroadcastingDone(t *testing.T) {
	preserveLifecycleGlobals(t)

	listener := newLifecycleListener(nil)
	updater := newLifecycleWorker()
	cacheWatcher := newLifecycleWorker()
	server, err := newServerFromConfig(
		lifecycleTestConfig(t),
		"/real/config.yaml",
		lifecycleTestRuntime(listener, updater, cacheWatcher),
	)
	if err != nil {
		t.Fatalf("newServerFromConfig() error = %v", err)
	}
	t.Cleanup(func() {
		updater.releaseNow()
		cacheWatcher.releaseNow()
		_ = server.Shutdown()
	})
	awaitLifecycleSignal(t, updater.started, "updater start")
	awaitLifecycleSignal(t, cacheWatcher.started, "watcher start")

	shutdownResult := make(chan error, 1)
	go func() { shutdownResult <- server.Shutdown() }()
	awaitLifecycleSignal(t, updater.canceled, "updater cancellation")
	awaitLifecycleSignal(t, cacheWatcher.canceled, "watcher cancellation")
	assertLifecycleNotDone(t, shutdownResult, "Shutdown returned before workers exited")
	assertLifecycleBroadcastOpen(t, server.Done(), "Done closed before workers exited")

	updater.releaseNow()
	awaitLifecycleSignal(t, updater.exited, "updater exit")
	assertLifecycleNotDone(t, shutdownResult, "Shutdown returned while watcher was still running")
	assertLifecycleBroadcastOpen(t, server.Done(), "Done closed while watcher was still running")

	cacheWatcher.releaseNow()
	awaitLifecycleSignal(t, cacheWatcher.exited, "watcher exit")
	select {
	case err := <-shutdownResult:
		if err != nil {
			t.Fatalf("Shutdown() error = %v, want nil", err)
		}
	case <-time.After(time.Second):
		t.Fatal("Shutdown() did not return after both workers exited")
	}
	awaitLifecycleSignal(t, server.Done(), "clean Done broadcast")
	if err := server.Err(); err != nil {
		t.Fatalf("Err() = %v after clean Shutdown, want nil", err)
	}
	if err := server.Err(); err != nil {
		t.Fatalf("repeated Err() = %v after clean Shutdown, want stable nil", err)
	}
}

func TestServerFatalAcceptCancelsAndWaitsForWorkersBeforeDone(t *testing.T) {
	preserveLifecycleGlobals(t)

	serveErr := errors.New("fatal accept")
	listener := newLifecycleListener(serveErr)
	updater := newLifecycleWorker()
	cacheWatcher := newLifecycleWorker()
	server, err := newServerFromConfig(
		lifecycleTestConfig(t),
		"/real/config.yaml",
		lifecycleTestRuntime(listener, updater, cacheWatcher),
	)
	if err != nil {
		t.Fatalf("newServerFromConfig() error = %v", err)
	}
	t.Cleanup(func() {
		listener.failNow()
		updater.releaseNow()
		cacheWatcher.releaseNow()
		_ = server.Shutdown()
	})
	awaitLifecycleSignal(t, updater.started, "updater start")
	awaitLifecycleSignal(t, cacheWatcher.started, "watcher start")

	listener.failNow()
	awaitLifecycleSignal(t, updater.canceled, "updater cancellation after serve failure")
	awaitLifecycleSignal(t, cacheWatcher.canceled, "watcher cancellation after serve failure")
	assertLifecycleBroadcastOpen(t, server.Done(), "Done closed before canceled workers exited")

	updater.releaseNow()
	awaitLifecycleSignal(t, updater.exited, "updater exit after serve failure")
	assertLifecycleBroadcastOpen(t, server.Done(), "Done closed while watcher was still running")

	cacheWatcher.releaseNow()
	awaitLifecycleSignal(t, cacheWatcher.exited, "watcher exit after serve failure")
	awaitLifecycleSignal(t, server.Done(), "fatal Done broadcast")
	firstErr := server.Err()
	if !errors.Is(firstErr, serveErr) {
		t.Fatalf("Err() = %v, want fatal accept error %v", firstErr, serveErr)
	}
	if secondErr := server.Err(); secondErr != firstErr {
		t.Fatalf("repeated Err() = %v, want stable terminal error %v", secondErr, firstErr)
	}
	if got := updater.running.Load(); got != 0 {
		t.Fatalf("updater running count = %d after Done, want 0", got)
	}
	if got := cacheWatcher.running.Load(); got != 0 {
		t.Fatalf("watcher running count = %d after Done, want 0", got)
	}
}

func TestServerFatalAcceptDrainsActiveHTTPHandlerBeforeDone(t *testing.T) {
	preserveLifecycleGlobals(t)

	serveErr := errors.New("fatal accept")
	listener, clientConn := newLifecycleHTTPFatalListener(serveErr)
	runtime := lifecycleNoopRuntime()
	runtime.listen = ListenFunc(func(string, string) (net.Listener, error) {
		return listener, nil
	})
	server, err := newServerFromConfig(
		lifecycleTestConfig(t),
		"/real/config.yaml",
		runtime,
	)
	if err != nil {
		t.Fatalf("newServerFromConfig() error = %v", err)
	}

	handlerStarted := make(chan struct{})
	handlerRelease := make(chan struct{})
	var handlerStartOnce, handlerReleaseOnce sync.Once
	server.httpServer.Handler = http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		handlerStartOnce.Do(func() { close(handlerStarted) })
		<-handlerRelease
		w.WriteHeader(http.StatusNoContent)
	})
	t.Cleanup(func() {
		handlerReleaseOnce.Do(func() { close(handlerRelease) })
		listener.failNow()
		_ = clientConn.Close()
		_ = server.Shutdown()
	})

	listener.acceptNow()
	clientResult := make(chan error, 1)
	go func() {
		if _, err := io.WriteString(clientConn, "GET / HTTP/1.1\r\nHost: lifecycle.test\r\nConnection: close\r\n\r\n"); err != nil {
			clientResult <- err
			return
		}
		_, err := io.Copy(io.Discard, clientConn)
		clientResult <- err
	}()
	awaitLifecycleSignal(t, listener.accepted, "first HTTP connection acceptance")
	awaitLifecycleSignal(t, handlerStarted, "blocking HTTP handler start")

	listener.failNow()
	awaitLifecycleSignal(t, listener.failed, "fatal second Accept")
	shutdownResult := make(chan error, 1)
	go func() { shutdownResult <- server.Shutdown() }()
	assertLifecycleBroadcastOpen(t, server.Done(), "Done closed while active HTTP handler was blocked")
	assertLifecycleNotDone(t, shutdownResult, "Shutdown returned while active HTTP handler was blocked")

	handlerReleaseOnce.Do(func() { close(handlerRelease) })
	awaitLifecycleSignal(t, server.Done(), "fatal Done broadcast after HTTP handler exit")
	if err := <-shutdownResult; err != nil {
		t.Fatalf("Shutdown() error = %v, want nil", err)
	}
	if err := <-clientResult; err != nil && !errors.Is(err, net.ErrClosed) {
		t.Fatalf("HTTP client connection error = %v", err)
	}
	firstErr := server.Err()
	if !errors.Is(firstErr, serveErr) {
		t.Fatalf("Err() = %v, want original fatal Accept error %v", firstErr, serveErr)
	}
	if secondErr := server.Err(); secondErr != firstErr {
		t.Fatalf("repeated Err() = %v, want stable terminal error %v", secondErr, firstErr)
	}
}

func TestServerShutdownTimeoutWaitsForActiveHTTPHandlerBeforeDone(t *testing.T) {
	preserveLifecycleGlobals(t)

	listener, clientConn := newLifecycleHTTPFatalListener(errors.New("unused fatal accept"))
	runtime := lifecycleNoopRuntime()
	runtime.listen = ListenFunc(func(string, string) (net.Listener, error) {
		return listener, nil
	})
	server, err := newServerFromConfig(
		lifecycleTestConfig(t),
		"/real/config.yaml",
		runtime,
	)
	if err != nil {
		t.Fatalf("newServerFromConfig() error = %v", err)
	}

	const shutdownTimeout = 25 * time.Millisecond
	server.shutdownTimeout = shutdownTimeout
	handlerStarted := make(chan struct{})
	handlerRelease := make(chan struct{})
	var handlerStartOnce, handlerReleaseOnce sync.Once
	server.httpServer.Handler = http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		handlerStartOnce.Do(func() { close(handlerStarted) })
		<-handlerRelease
		w.WriteHeader(http.StatusNoContent)
	})
	t.Cleanup(func() {
		handlerReleaseOnce.Do(func() { close(handlerRelease) })
		listener.failNow()
		_ = clientConn.Close()
		_ = server.Shutdown()
	})

	listener.acceptNow()
	clientResult := make(chan error, 1)
	go func() {
		if _, err := io.WriteString(clientConn, "GET / HTTP/1.1\r\nHost: lifecycle.test\r\nConnection: close\r\n\r\n"); err != nil {
			clientResult <- err
			return
		}
		_, err := io.Copy(io.Discard, clientConn)
		clientResult <- err
	}()
	awaitLifecycleSignal(t, listener.accepted, "HTTP connection acceptance")
	awaitLifecycleSignal(t, handlerStarted, "blocking HTTP handler start")

	shutdownResult := make(chan error, 1)
	go func() { shutdownResult <- server.Shutdown() }()
	awaitLifecycleSignal(t, listener.closed, "listener close during Shutdown")
	timer := time.NewTimer(4 * shutdownTimeout)
	select {
	case err := <-shutdownResult:
		timer.Stop()
		t.Fatalf("Shutdown() returned after timeout while active HTTP handler was blocked: %v", err)
	case <-server.Done():
		timer.Stop()
		t.Fatal("Done closed after timeout while active HTTP handler was blocked")
	case <-timer.C:
	}
	assertLifecycleBroadcastOpen(t, server.Done(), "Done closed after timeout while active HTTP handler was blocked")
	assertLifecycleNotDone(t, shutdownResult, "Shutdown returned after timeout while active HTTP handler was blocked")

	handlerReleaseOnce.Do(func() { close(handlerRelease) })
	var shutdownErr error
	select {
	case shutdownErr = <-shutdownResult:
	case <-time.After(time.Second):
		t.Fatal("Shutdown() did not return after active HTTP handler exited")
	}
	if !errors.Is(shutdownErr, context.DeadlineExceeded) {
		t.Fatalf("Shutdown() error = %v, want context deadline exceeded", shutdownErr)
	}
	awaitLifecycleSignal(t, server.Done(), "clean Done broadcast after HTTP handler exit")
	if err := <-clientResult; err != nil && !errors.Is(err, net.ErrClosed) {
		t.Fatalf("HTTP client connection error = %v", err)
	}
	if err := server.Err(); err != nil {
		t.Fatalf("Err() = %v after timed-out clean Shutdown, want nil", err)
	}
	if err := server.Err(); err != nil {
		t.Fatalf("repeated Err() = %v after timed-out clean Shutdown, want stable nil", err)
	}
	if err := server.Shutdown(); err != shutdownErr {
		t.Fatalf("repeated Shutdown() error = %v, want stable error %v", err, shutdownErr)
	}
}

func lifecycleNoopRuntime() serverRuntime {
	return serverRuntime{
		initialize: func(server *Server, _ *global.Config, _ string) error {
			server.logger = zap.NewNop()
			return nil
		},
		runUpdater: func(context.Context, *global.Config) {},
		runWatcher: func(context.Context, *global.Config) {},
	}
}

func lifecycleTestRuntime(listener net.Listener, updater, cacheWatcher *lifecycleWorker) serverRuntime {
	runtime := lifecycleNoopRuntime()
	runtime.listen = ListenFunc(func(string, string) (net.Listener, error) {
		return listener, nil
	})
	runtime.runUpdater = updater.run
	runtime.runWatcher = cacheWatcher.run
	return runtime
}

func lifecycleTestConfig(t *testing.T) *global.Config {
	t.Helper()
	return &global.Config{
		Server: global.ServerConfig{
			Port:         9716,
			ReadTimeout:  1,
			WriteTimeout: 1,
			IdleTimeout:  1,
		},
		Auth: global.AuthConfig{Password: "890716"},
		Subscription: global.SubscriptionConfig{
			URL:             "http://127.0.0.1:1/not-requested",
			Timeout:         1,
			RefreshInterval: 1,
		},
		Templates: map[string]global.TemplateConfig{
			"default": {
				URL:     "http://127.0.0.1:1/not-requested",
				Name:    "default",
				Enabled: true,
			},
		},
		DefaultTemplate: "default",
		Cache: global.CacheConfig{
			Directory:    t.TempDir(),
			NodeFile:     "nodes.json",
			TemplateFile: "template.json",
		},
		Logging: global.LoggingConfig{Level: "info"},
	}
}

func preserveLifecycleGlobals(t *testing.T) {
	t.Helper()
	previousCfg, previousPath, previousLogger := global.Cfg, global.ConfigFile, global.Logger
	t.Cleanup(func() { global.Cfg, global.ConfigFile, global.Logger = previousCfg, previousPath, previousLogger })
}

type lifecycleListener struct {
	acceptErr error
	fail      chan struct{}
	closed    chan struct{}
	failOnce  sync.Once
	closeOnce sync.Once
	accepts   atomic.Int32
}

func newLifecycleListener(acceptErr error) *lifecycleListener {
	return &lifecycleListener{
		acceptErr: acceptErr,
		fail:      make(chan struct{}),
		closed:    make(chan struct{}),
	}
}

func (l *lifecycleListener) Accept() (net.Conn, error) {
	l.accepts.Add(1)
	select {
	case <-l.fail:
		return nil, l.acceptErr
	case <-l.closed:
		return nil, net.ErrClosed
	}
}

func (l *lifecycleListener) Close() error {
	l.closeOnce.Do(func() { close(l.closed) })
	return nil
}

func (l *lifecycleListener) Addr() net.Addr { return lifecycleAddr(":9716") }

func (l *lifecycleListener) failNow() {
	l.failOnce.Do(func() { close(l.fail) })
}

type lifecycleHTTPFatalListener struct {
	serverConn net.Conn
	accept     chan struct{}
	accepted   chan struct{}
	fail       chan struct{}
	failed     chan struct{}
	closed     chan struct{}
	acceptErr  error
	accepts    atomic.Int32
	acceptOnce sync.Once
	failOnce   sync.Once
	closeOnce  sync.Once
}

func newLifecycleHTTPFatalListener(acceptErr error) (*lifecycleHTTPFatalListener, net.Conn) {
	serverConn, clientConn := net.Pipe()
	return &lifecycleHTTPFatalListener{
		serverConn: serverConn,
		accept:     make(chan struct{}),
		accepted:   make(chan struct{}),
		fail:       make(chan struct{}),
		failed:     make(chan struct{}),
		closed:     make(chan struct{}),
		acceptErr:  acceptErr,
	}, clientConn
}

func (l *lifecycleHTTPFatalListener) Accept() (net.Conn, error) {
	if l.accepts.Add(1) == 1 {
		select {
		case <-l.accept:
			close(l.accepted)
			return l.serverConn, nil
		case <-l.closed:
			return nil, net.ErrClosed
		}
	}

	select {
	case <-l.fail:
		close(l.failed)
		return nil, l.acceptErr
	case <-l.closed:
		return nil, net.ErrClosed
	}
}

func (l *lifecycleHTTPFatalListener) Close() error {
	l.closeOnce.Do(func() { close(l.closed) })
	return nil
}

func (l *lifecycleHTTPFatalListener) Addr() net.Addr { return lifecycleAddr(":9716") }

func (l *lifecycleHTTPFatalListener) acceptNow() {
	l.acceptOnce.Do(func() { close(l.accept) })
}

func (l *lifecycleHTTPFatalListener) failNow() {
	l.failOnce.Do(func() { close(l.fail) })
}

type lifecycleAddr string

func (lifecycleAddr) Network() string  { return "tcp" }
func (a lifecycleAddr) String() string { return string(a) }

type lifecycleWorker struct {
	started     chan struct{}
	canceled    chan struct{}
	exited      chan struct{}
	release     chan struct{}
	startOnce   sync.Once
	cancelOnce  sync.Once
	exitOnce    sync.Once
	releaseOnce sync.Once
	running     atomic.Int32
}

func newLifecycleWorker() *lifecycleWorker {
	return &lifecycleWorker{
		started:  make(chan struct{}),
		canceled: make(chan struct{}),
		exited:   make(chan struct{}),
		release:  make(chan struct{}),
	}
}

func (w *lifecycleWorker) run(ctx context.Context, _ *global.Config) {
	w.running.Add(1)
	w.startOnce.Do(func() { close(w.started) })
	<-ctx.Done()
	w.cancelOnce.Do(func() { close(w.canceled) })
	<-w.release
	w.running.Add(-1)
	w.exitOnce.Do(func() { close(w.exited) })
}

func (w *lifecycleWorker) releaseNow() {
	w.releaseOnce.Do(func() { close(w.release) })
}

func awaitLifecycleSignal(t *testing.T, signal <-chan struct{}, description string) {
	t.Helper()
	select {
	case <-signal:
	case <-time.After(time.Second):
		t.Fatalf("timed out waiting for %s", description)
	}
}

func assertLifecycleBroadcastOpen(t *testing.T, done <-chan struct{}, message string) {
	t.Helper()
	select {
	case <-done:
		t.Fatal(message)
	case <-time.After(20 * time.Millisecond):
	}
}

func assertLifecycleNotDone(t *testing.T, result <-chan error, message string) {
	t.Helper()
	select {
	case err := <-result:
		t.Fatalf("%s: %v", message, err)
	case <-time.After(20 * time.Millisecond):
	}
}
