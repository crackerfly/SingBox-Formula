package cmd

import (
	"errors"
	"fmt"
	"sync"

	"github.com/haierkeys/singbox-subscribe-convert/global"
)

// serverInstance is the lifecycle surface owned by ServerSupervisor.
//
// Implementations must always return the same non-nil channel from Done and
// close it exactly once on both clean and fatal termination. The close is the
// terminal broadcast: it must happen only after every owned resource (including
// listeners, serving loops, watchers, tickers, and their goroutines) has exited.
// After Done closes, Err must be safe to call repeatedly and concurrently and
// must always return the same terminal error (nil for a clean stop). Shutdown
// must cause and wait for that same terminal state; it must not consume a
// one-shot completion value that could race with Done observers.
type serverInstance interface {
	Done() <-chan struct{}
	Err() error
	Shutdown() error
}

type candidateLoader func(string) (*global.Config, string, error)
type serverFactory func(*global.Config, string) (serverInstance, error)

type ownedServer struct {
	serverInstance
	observed chan struct{}
}

// ServerSupervisor serializes ownership and replacement of the current server.
type ServerSupervisor struct {
	configPath string
	load       candidateLoader
	newServer  serverFactory

	mu      sync.Mutex
	current *ownedServer
	closed  bool

	terminalMu  sync.RWMutex
	terminalErr error

	done     chan struct{}
	doneOnce sync.Once

	shutdownOnce sync.Once
	shutdownDone chan struct{}
	shutdownErr  error
}

func newServerSupervisor(configPath string, load candidateLoader, factory serverFactory) *ServerSupervisor {
	return &ServerSupervisor{
		configPath:   configPath,
		load:         load,
		newServer:    factory,
		done:         make(chan struct{}),
		shutdownDone: make(chan struct{}),
	}
}

// Reload validates a candidate before disturbing the current server. Once a
// candidate is valid, the old instance is stopped completely before the new
// instance is started, so same-address replacements cannot overlap listeners.
func (s *ServerSupervisor) Reload() error {
	s.mu.Lock()
	defer s.mu.Unlock()

	if s.closed {
		return errors.New("server supervisor is shut down")
	}
	if fatalErr := s.fatalError(); fatalErr != nil {
		return fmt.Errorf("server supervisor is terminal: %w", fatalErr)
	}

	cfg, realpath, err := s.load(s.configPath)
	if err != nil {
		err = fmt.Errorf("load candidate config: %w", err)
		if s.current == nil {
			s.fail(err)
		}
		return err
	}

	if s.current != nil {
		current := s.current
		s.current = nil
		shutdownErr := current.Shutdown()
		<-current.observed
		if shutdownErr != nil {
			err = fmt.Errorf("shutdown current server: %w", shutdownErr)
			s.fail(err)
			return err
		}
		if fatalErr := s.fatalError(); fatalErr != nil {
			return fmt.Errorf("current server stopped: %w", fatalErr)
		}
	}

	next, err := s.newServer(cfg, realpath)
	if err != nil {
		err = fmt.Errorf("start candidate server: %w", err)
		s.fail(err)
		return err
	}
	if next == nil {
		err = errors.New("start candidate server: factory returned nil server")
		s.fail(err)
		return err
	}

	owned := &ownedServer{serverInstance: next, observed: make(chan struct{})}
	s.current = owned
	go s.watch(owned)
	return nil
}

// Done returns a close-only terminal broadcast channel. Every observer sees the
// same close; after it closes, Err reports the stable terminal result.
func (s *ServerSupervisor) Done() <-chan struct{} {
	return s.done
}

// Err returns the terminal lifecycle error, or nil after a clean shutdown. Its
// result is stable once Done has closed and is safe for concurrent callers.
func (s *ServerSupervisor) Err() error {
	s.terminalMu.RLock()
	defer s.terminalMu.RUnlock()
	return s.terminalErr
}

// Shutdown is idempotent. Every caller waits for the one current instance to
// finish shutting down before it returns.
func (s *ServerSupervisor) Shutdown() error {
	s.shutdownOnce.Do(func() {
		s.mu.Lock()
		s.closed = true
		if s.current != nil {
			current := s.current
			s.current = nil
			s.shutdownErr = current.Shutdown()
			<-current.observed
		}
		if s.shutdownErr != nil {
			s.fail(fmt.Errorf("shutdown current server: %w", s.shutdownErr))
		} else {
			s.finish(nil)
		}
		s.mu.Unlock()
		close(s.shutdownDone)
	})

	<-s.shutdownDone
	return s.shutdownErr
}

func (s *ServerSupervisor) watch(instance *ownedServer) {
	<-instance.Done()
	if err := instance.Err(); err != nil {
		s.fail(fmt.Errorf("current server stopped: %w", err))
	}
	close(instance.observed)
}

func (s *ServerSupervisor) fail(err error) {
	s.finish(err)
}

func (s *ServerSupervisor) fatalError() error {
	return s.Err()
}

func (s *ServerSupervisor) finish(err error) {
	s.doneOnce.Do(func() {
		s.terminalMu.Lock()
		s.terminalErr = err
		s.terminalMu.Unlock()
		close(s.done)
	})
}
