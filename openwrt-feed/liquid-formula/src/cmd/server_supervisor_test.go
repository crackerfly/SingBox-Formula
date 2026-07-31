package cmd

import (
	"errors"
	"fmt"
	"net"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/haierkeys/singbox-subscribe-convert/global"
)

func TestSupervisorInvalidConfigKeepsCurrentServer(t *testing.T) {
	var loadCalls atomic.Int32
	load := func(string) (*global.Config, string, error) {
		if loadCalls.Add(1) == 1 {
			return supervisorTestConfig(9716), "/real/config.yaml", nil
		}
		return nil, "", errors.New("invalid candidate")
	}
	var current *supervisorFakeServer
	factory := func(*global.Config, string) (serverInstance, error) {
		current = newSupervisorFakeServer(nil)
		return current, nil
	}

	supervisor := newServerSupervisor("config.yaml", load, factory)
	if err := supervisor.Reload(); err != nil {
		t.Fatalf("initial Reload() error = %v", err)
	}
	t.Cleanup(func() { _ = supervisor.Shutdown() })

	if err := supervisor.Reload(); err == nil {
		t.Fatal("Reload() error = nil, want candidate validation error")
	}
	if got := current.shutdownCalls.Load(); got != 0 {
		t.Fatalf("current server shutdown calls = %d, want 0", got)
	}
	select {
	case <-supervisor.Done():
		t.Fatalf("invalid candidate made supervisor terminal: %v", supervisor.Err())
	default:
	}
}

func TestSupervisorReloadSamePortThreeTimes(t *testing.T) {
	initialListener, err := net.Listen("tcp", ":0")
	if err != nil {
		t.Fatalf("listen on ephemeral port: %v", err)
	}
	t.Cleanup(func() { _ = initialListener.Close() })
	port := initialListener.Addr().(*net.TCPAddr).Port
	address := fmt.Sprintf(":%d", port)

	load := func(string) (*global.Config, string, error) {
		return supervisorTestConfig(port), "/real/config.yaml", nil
	}
	var (
		mu      sync.Mutex
		servers []*supervisorFakeServer
	)
	factory := func(cfg *global.Config, _ string) (serverInstance, error) {
		var listener net.Listener
		if len(servers) == 0 {
			if cfg.Server.Port != port {
				return nil, fmt.Errorf("initial port = %d, want %d", cfg.Server.Port, port)
			}
			listener = initialListener
		} else {
			listener, err = net.Listen("tcp", address)
			if err != nil {
				return nil, err
			}
		}
		server := newSupervisorFakeServer(listener)
		mu.Lock()
		servers = append(servers, server)
		mu.Unlock()
		return server, nil
	}

	supervisor := newServerSupervisor("config.yaml", load, factory)
	if err := supervisor.Reload(); err != nil {
		t.Fatalf("initial Reload() error = %v", err)
	}
	t.Cleanup(func() { _ = supervisor.Shutdown() })
	for reload := 1; reload <= 3; reload++ {
		if err := supervisor.Reload(); err != nil {
			t.Fatalf("Reload() #%d error = %v", reload, err)
		}
	}

	mu.Lock()
	defer mu.Unlock()
	if got := len(servers); got != 4 {
		t.Fatalf("created servers = %d, want 4", got)
	}
	for i, server := range servers[:3] {
		if got := server.shutdownCalls.Load(); got != 1 {
			t.Errorf("replaced server %d shutdown calls = %d, want 1", i, got)
		}
	}
}

func TestSupervisorReloadSamePortThreeTimesWithRealServer(t *testing.T) {
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

	load := func(string) (*global.Config, string, error) {
		cfg := lifecycleTestConfig(t)
		cfg.Server.Port = port
		return cfg, "/real/config.yaml", nil
	}
	var serverCount atomic.Int32
	factory := func(cfg *global.Config, realpath string) (serverInstance, error) {
		runtime := lifecycleNoopRuntime()
		runtime.listen = ListenFunc(func(network, gotAddress string) (net.Listener, error) {
			if network != "tcp" {
				return nil, fmt.Errorf("listen network = %q, want tcp", network)
			}
			if gotAddress != address {
				return nil, fmt.Errorf("listen address = %q, want %q", gotAddress, address)
			}
			return net.Listen(network, gotAddress)
		})
		server, err := newServerFromConfig(cfg, realpath, runtime)
		if err == nil {
			serverCount.Add(1)
		}
		return server, err
	}

	supervisor := newServerSupervisor("config.yaml", load, factory)
	if err := supervisor.Reload(); err != nil {
		t.Fatalf("initial Reload() error = %v", err)
	}
	t.Cleanup(func() { _ = supervisor.Shutdown() })
	for reload := 1; reload <= 3; reload++ {
		if err := supervisor.Reload(); err != nil {
			t.Fatalf("Reload() #%d error = %v", reload, err)
		}
	}
	if got := serverCount.Load(); got != 4 {
		t.Fatalf("real servers created = %d, want 4", got)
	}
}

func TestSupervisorReloadBindFailurePropagates(t *testing.T) {
	bindErr := errors.New("bind failed")
	load := func(string) (*global.Config, string, error) {
		return supervisorTestConfig(9716), "/real/config.yaml", nil
	}
	var factoryCalls atomic.Int32
	current := newSupervisorFakeServer(nil)
	factory := func(*global.Config, string) (serverInstance, error) {
		if factoryCalls.Add(1) == 1 {
			return current, nil
		}
		return nil, bindErr
	}

	supervisor := newServerSupervisor("config.yaml", load, factory)
	if err := supervisor.Reload(); err != nil {
		t.Fatalf("initial Reload() error = %v", err)
	}
	err := supervisor.Reload()
	if !errors.Is(err, bindErr) {
		t.Fatalf("Reload() error = %v, want %v", err, bindErr)
	}
	select {
	case <-supervisor.Done():
		fatalErr := supervisor.Err()
		if !errors.Is(fatalErr, bindErr) {
			t.Fatalf("Err() = %v, want %v", fatalErr, bindErr)
		}
	case <-time.After(time.Second):
		t.Fatal("Done() did not broadcast fatal replacement failure")
	}
	if got := current.shutdownCalls.Load(); got != 1 {
		t.Fatalf("current server shutdown calls = %d, want 1", got)
	}
	if err := supervisor.Reload(); err == nil {
		t.Fatal("Reload() after fatal error = nil, want terminal supervisor error")
	}
	if got := factoryCalls.Load(); got != 2 {
		t.Fatalf("factory calls after fatal Reload() = %d, want 2", got)
	}
}

func TestSupervisorServerFatalRacingWithReloadIsTerminal(t *testing.T) {
	serveErr := errors.New("serve failed")
	load := func(string) (*global.Config, string, error) {
		return supervisorTestConfig(9716), "/real/config.yaml", nil
	}
	current := newSupervisorFakeServer(nil)
	current.doneErr = serveErr
	var factoryCalls atomic.Int32
	factory := func(*global.Config, string) (serverInstance, error) {
		factoryCalls.Add(1)
		return current, nil
	}

	supervisor := newServerSupervisor("config.yaml", load, factory)
	if err := supervisor.Reload(); err != nil {
		t.Fatalf("initial Reload() error = %v", err)
	}
	if err := supervisor.Reload(); !errors.Is(err, serveErr) {
		t.Fatalf("racing Reload() error = %v, want %v", err, serveErr)
	}
	select {
	case <-supervisor.Done():
		fatalErr := supervisor.Err()
		if !errors.Is(fatalErr, serveErr) {
			t.Fatalf("Err() = %v, want %v", fatalErr, serveErr)
		}
	case <-time.After(time.Second):
		t.Fatal("Done() did not broadcast racing server fatal")
	}
	if err := supervisor.Reload(); err == nil {
		t.Fatal("Reload() after racing fatal = nil, want terminal error")
	}
	if got := factoryCalls.Load(); got != 1 {
		t.Fatalf("factory calls = %d, want 1", got)
	}
}

func TestSupervisorDoneBroadcastsFatalToConcurrentObservers(t *testing.T) {
	bindErr := errors.New("bind failed")
	load := func(string) (*global.Config, string, error) {
		return supervisorTestConfig(9716), "/real/config.yaml", nil
	}
	factory := func(*global.Config, string) (serverInstance, error) {
		return nil, bindErr
	}
	supervisor := newServerSupervisor("config.yaml", load, factory)

	results := startSupervisorObservers(t, supervisor, 8)
	if err := supervisor.Reload(); !errors.Is(err, bindErr) {
		t.Fatalf("Reload() error = %v, want %v", err, bindErr)
	}
	for i, result := range results {
		select {
		case err := <-result:
			if !errors.Is(err, bindErr) {
				t.Fatalf("observer %d Err() = %v, want %v", i, err, bindErr)
			}
		case <-time.After(time.Second):
			t.Fatalf("observer %d did not see terminal broadcast", i)
		}
	}
	if err := supervisor.Err(); !errors.Is(err, bindErr) {
		t.Fatalf("repeated Err() = %v, want stable %v", err, bindErr)
	}
}

func TestSupervisorDoneBroadcastsCleanShutdownToConcurrentObservers(t *testing.T) {
	load := func(string) (*global.Config, string, error) {
		return supervisorTestConfig(9716), "/real/config.yaml", nil
	}
	current := newSupervisorFakeServer(nil)
	factory := func(*global.Config, string) (serverInstance, error) {
		return current, nil
	}
	supervisor := newServerSupervisor("config.yaml", load, factory)
	if err := supervisor.Reload(); err != nil {
		t.Fatalf("initial Reload() error = %v", err)
	}

	results := startSupervisorObservers(t, supervisor, 8)
	if err := supervisor.Shutdown(); err != nil {
		t.Fatalf("Shutdown() error = %v", err)
	}
	for i, result := range results {
		select {
		case err := <-result:
			if err != nil {
				t.Fatalf("observer %d Err() = %v, want clean terminal state", i, err)
			}
		case <-time.After(time.Second):
			t.Fatalf("observer %d did not see clean terminal broadcast", i)
		}
	}
	if err := supervisor.Err(); err != nil {
		t.Fatalf("repeated Err() = %v, want stable nil", err)
	}
}

func TestSupervisorShutdownIsIdempotentAndWaits(t *testing.T) {
	load := func(string) (*global.Config, string, error) {
		return supervisorTestConfig(9716), "/real/config.yaml", nil
	}
	release := make(chan struct{})
	current := newSupervisorFakeServer(nil)
	current.shutdownBlock = release
	factory := func(*global.Config, string) (serverInstance, error) {
		return current, nil
	}
	supervisor := newServerSupervisor("config.yaml", load, factory)
	if err := supervisor.Reload(); err != nil {
		t.Fatalf("initial Reload() error = %v", err)
	}

	firstDone := make(chan error, 1)
	go func() { firstDone <- supervisor.Shutdown() }()
	deadline := time.After(time.Second)
	for current.shutdownCalls.Load() == 0 {
		select {
		case <-deadline:
			t.Fatal("Shutdown() did not start current server shutdown")
		default:
			time.Sleep(time.Millisecond)
		}
	}
	secondDone := make(chan error, 1)
	go func() { secondDone <- supervisor.Shutdown() }()
	select {
	case err := <-secondDone:
		t.Fatalf("second Shutdown() returned before current stopped: %v", err)
	case <-time.After(20 * time.Millisecond):
	}
	close(release)
	for i, done := range []<-chan error{firstDone, secondDone} {
		select {
		case err := <-done:
			if err != nil {
				t.Fatalf("Shutdown() call %d error = %v", i+1, err)
			}
		case <-time.After(time.Second):
			t.Fatalf("Shutdown() call %d did not finish", i+1)
		}
	}
	if got := current.shutdownCalls.Load(); got != 1 {
		t.Fatalf("current server shutdown calls = %d, want 1", got)
	}
}

func supervisorTestConfig(port int) *global.Config {
	return &global.Config{Server: global.ServerConfig{Port: port}}
}

func startSupervisorObservers(t *testing.T, supervisor *ServerSupervisor, count int) []<-chan error {
	t.Helper()

	ready := make(chan struct{}, count)
	results := make([]<-chan error, 0, count)
	for i := 0; i < count; i++ {
		result := make(chan error, 1)
		results = append(results, result)
		go func() {
			done := supervisor.Done()
			ready <- struct{}{}
			<-done
			result <- supervisor.Err()
		}()
	}
	for i := 0; i < count; i++ {
		select {
		case <-ready:
		case <-time.After(time.Second):
			t.Fatalf("observer %d did not start", i)
		}
	}
	return results
}

type supervisorFakeServer struct {
	listener      net.Listener
	done          chan struct{}
	doneErr       error
	shutdownOnce  sync.Once
	shutdownCalls atomic.Int32
	shutdownBlock <-chan struct{}
}

func newSupervisorFakeServer(listener net.Listener) *supervisorFakeServer {
	return &supervisorFakeServer{listener: listener, done: make(chan struct{})}
}

func (s *supervisorFakeServer) Done() <-chan struct{} {
	return s.done
}

func (s *supervisorFakeServer) Err() error { return s.doneErr }

func (s *supervisorFakeServer) Shutdown() error {
	s.shutdownOnce.Do(func() {
		s.shutdownCalls.Add(1)
		if s.shutdownBlock != nil {
			<-s.shutdownBlock
		}
		if s.listener != nil {
			_ = s.listener.Close()
		}
		close(s.done)
	})
	return nil
}
