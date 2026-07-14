package watcher

import (
	"context"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"github.com/haierkeys/singbox-subscribe-convert/global"
	"go.uber.org/zap"
)

func TestWatcherDebouncesWriteCreateAndRealAtomicRename(t *testing.T) {
	for _, test := range []struct {
		name   string
		mutate func(t *testing.T, target string)
	}{
		{name: "write burst", mutate: func(t *testing.T, target string) {
			for _, value := range []string{"one", "two", "three"} {
				if err := os.WriteFile(target, []byte(value), 0o600); err != nil {
					t.Fatal(err)
				}
			}
		}},
		{name: "create", mutate: func(t *testing.T, target string) {
			if err := os.Remove(target); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(target, []byte("created"), 0o600); err != nil {
				t.Fatal(err)
			}
		}},
		{name: "atomic rename", mutate: func(t *testing.T, target string) {
			stage := filepath.Join(filepath.Dir(target), ".atomic-stage")
			if err := os.WriteFile(stage, []byte("renamed"), 0o600); err != nil {
				t.Fatal(err)
			}
			if err := os.Rename(stage, target); err != nil {
				t.Fatal(err)
			}
		}},
		{name: "rename event", mutate: func(t *testing.T, target string) {
			if err := os.Rename(target, target+".moved"); err != nil {
				t.Fatal(err)
			}
		}},
	} {
		t.Run(test.name, func(t *testing.T) {
			config, target := watcherTestConfig(t)
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			ready := make(chan struct{})
			called := make(chan struct{}, 4)
			done := make(chan error, 1)
			go func() {
				done <- StartWithOptions(ctx, config, zap.NewNop(), func(context.Context) error {
					called <- struct{}{}
					return nil
				}, func(context.Context, string) error { return nil }, Options{Debounce: 25 * time.Millisecond, Ready: ready})
			}()
			<-ready

			test.mutate(t, target)
			select {
			case <-called:
			case <-time.After(time.Second):
				t.Fatal("watcher callback did not run")
			}
			select {
			case <-called:
				t.Fatal("burst produced more than one debounced callback")
			case <-time.After(60 * time.Millisecond):
			}
			cancel()
			if err := <-done; err != nil {
				t.Fatalf("StartWithOptions() error = %v", err)
			}
		})
	}
}

func TestWatcherIgnoresUnrelatedOperationsAndHasNoLateCallbackAfterCancel(t *testing.T) {
	config, target := watcherTestConfig(t)
	ctx, cancel := context.WithCancel(context.Background())
	ready := make(chan struct{})
	var calls atomic.Int32
	done := make(chan error, 1)
	go func() {
		done <- StartWithOptions(ctx, config, zap.NewNop(), func(context.Context) error {
			calls.Add(1)
			return nil
		}, func(context.Context, string) error { return nil }, Options{Debounce: 50 * time.Millisecond, Ready: ready})
	}()
	<-ready

	unrelated := filepath.Join(config.Cache.Directory, "unrelated.json")
	if err := os.WriteFile(unrelated, []byte("ignored"), 0o600); err != nil {
		t.Fatal(err)
	}
	time.Sleep(75 * time.Millisecond)
	if got := calls.Load(); got != 0 {
		t.Fatalf("callbacks for unrelated file = %d, want 0", got)
	}
	if err := os.WriteFile(target, []byte("pending"), 0o600); err != nil {
		t.Fatal(err)
	}
	cancel()
	if err := <-done; err != nil {
		t.Fatalf("StartWithOptions() error = %v", err)
	}
	time.Sleep(75 * time.Millisecond)
	if got := calls.Load(); got != 0 {
		t.Fatalf("callbacks after cancellation = %d, want 0", got)
	}
}

func TestWatcherCancellationEndsActiveContextAwareCallbackBeforeReturn(t *testing.T) {
	config, target := watcherTestConfig(t)
	ctx, cancel := context.WithCancel(context.Background())
	ready := make(chan struct{})
	callbackStarted := make(chan struct{})
	callbackDone := make(chan struct{})
	done := make(chan error, 1)
	go func() {
		done <- StartWithOptions(ctx, config, zap.NewNop(), func(callbackCtx context.Context) error {
			close(callbackStarted)
			<-callbackCtx.Done()
			close(callbackDone)
			return callbackCtx.Err()
		}, func(context.Context, string) error { return nil }, Options{Debounce: 10 * time.Millisecond, Ready: ready})
	}()
	<-ready
	if err := os.WriteFile(target, []byte("changed"), 0o600); err != nil {
		t.Fatal(err)
	}
	select {
	case <-callbackStarted:
	case <-time.After(time.Second):
		t.Fatal("callback did not start")
	}
	cancel()
	if err := <-done; err != nil {
		t.Fatalf("StartWithOptions() error = %v", err)
	}
	select {
	case <-callbackDone:
	default:
		t.Fatal("StartWithOptions returned before active callback ended")
	}
}

func watcherTestConfig(t *testing.T) (*global.Config, string) {
	t.Helper()
	directory := t.TempDir()
	config := &global.Config{
		Cache: global.CacheConfig{Directory: directory, NodeFile: "nodes.json"},
		Templates: map[string]global.TemplateConfig{
			"default": {Enabled: true},
		},
	}
	target := config.GetNodeFilePath()
	if err := os.WriteFile(target, []byte("initial"), 0o600); err != nil {
		t.Fatal(err)
	}
	return config, target
}
