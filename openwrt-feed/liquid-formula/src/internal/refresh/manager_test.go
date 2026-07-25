package refresh

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func TestCoordinatorAllowsOneMaximumWriterAcrossAllTriggers(t *testing.T) {
	manager := NewManager()
	triggers := []string{"initial", "manual", "query", "auto", "on-demand"}
	start := make(chan struct{})
	release := make(chan struct{})
	entered := make(chan string, len(triggers))
	var active atomic.Int32
	var maximum atomic.Int32
	var wg sync.WaitGroup

	for _, trigger := range triggers {
		trigger := trigger
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			if err := manager.Do(context.Background(), trigger, func(context.Context) error {
				current := active.Add(1)
				for {
					old := maximum.Load()
					if current <= old || maximum.CompareAndSwap(old, current) {
						break
					}
				}
				entered <- trigger
				<-release
				active.Add(-1)
				return nil
			}); err != nil {
				t.Errorf("Do(%s) error = %v", trigger, err)
			}
		}()
	}

	close(start)
	for range triggers {
		select {
		case <-entered:
		case <-time.After(time.Second):
			t.Fatal("timed out waiting for serialized writer")
		}
		select {
		case trigger := <-entered:
			t.Fatalf("writer %q entered while the permit was still held", trigger)
		case <-time.After(15 * time.Millisecond):
		}
		release <- struct{}{}
	}
	wg.Wait()
	if got := maximum.Load(); got != 1 {
		t.Fatalf("maximum active writers = %d, want 1", got)
	}
}

func TestCoordinatorWaitingCancellationDoesNotRunWork(t *testing.T) {
	manager := NewManager()
	firstEntered := make(chan struct{})
	releaseFirst := make(chan struct{})
	firstDone := make(chan error, 1)
	go func() {
		firstDone <- manager.Do(context.Background(), "initial", func(context.Context) error {
			close(firstEntered)
			<-releaseFirst
			return nil
		})
	}()
	<-firstEntered

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	var called atomic.Bool
	err := manager.Do(ctx, "manual", func(context.Context) error {
		called.Store(true)
		return nil
	})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled waiter error = %v, want context.Canceled", err)
	}
	if called.Load() {
		t.Fatal("canceled waiter ran its work")
	}

	close(releaseFirst)
	if err := <-firstDone; err != nil {
		t.Fatalf("first writer error = %v", err)
	}
	if err := manager.Do(context.Background(), "query", func(context.Context) error { return nil }); err != nil {
		t.Fatalf("writer after canceled waiter error = %v", err)
	}
}

func TestCoordinatorActiveCancellationReleasesPermit(t *testing.T) {
	manager := NewManager()
	ctx, cancel := context.WithCancel(context.Background())
	entered := make(chan struct{})
	done := make(chan error, 1)
	go func() {
		done <- manager.Do(ctx, "auto", func(workCtx context.Context) error {
			close(entered)
			<-workCtx.Done()
			return workCtx.Err()
		})
	}()
	<-entered
	cancel()
	if err := <-done; !errors.Is(err, context.Canceled) {
		t.Fatalf("active canceled writer error = %v, want context.Canceled", err)
	}
	if err := manager.Do(context.Background(), "on-demand", func(context.Context) error { return nil }); err != nil {
		t.Fatalf("writer after active cancellation error = %v", err)
	}
}
