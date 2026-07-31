package main

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"
)

func TestSubscriptionLockCreatesAndReleasesSecureFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "subscription.lock")
	locker := newFileSubscriptionLocker(path, 10*time.Millisecond)
	held, err := locker.Acquire(context.Background())
	if err != nil {
		t.Fatalf("first acquire: %v", err)
	}
	info, err := os.Lstat(path)
	if err != nil {
		t.Fatalf("lstat lock: %v", err)
	}
	if !info.Mode().IsRegular() || info.Mode().Perm() != 0600 {
		t.Fatalf("created lock mode = %v", info.Mode())
	}
	if err := held.Release(); err != nil {
		t.Fatalf("release: %v", err)
	}
	second, err := locker.Acquire(context.Background())
	if err != nil {
		t.Fatalf("reacquire: %v", err)
	}
	if err := second.Release(); err != nil {
		t.Fatalf("second release: %v", err)
	}
}

func TestSubscriptionLockContentionReturnsBeforeDeadlineByAtMostRetry(t *testing.T) {
	path := filepath.Join(t.TempDir(), "subscription.lock")
	const retry = 100 * time.Millisecond
	locker := newFileSubscriptionLocker(path, retry)
	owner, err := locker.Acquire(context.Background())
	if err != nil {
		t.Fatalf("owner acquire: %v", err)
	}
	defer owner.Release()

	const budget = 450 * time.Millisecond
	ctx, cancel := context.WithTimeout(context.Background(), budget)
	defer cancel()
	started := time.Now()
	held, err := locker.Acquire(ctx)
	elapsed := time.Since(started)
	if err == nil {
		_ = held.Release()
		t.Fatal("contended lock unexpectedly acquired")
	}
	if elapsed >= budget {
		t.Fatalf("busy returned after deadline: elapsed=%v budget=%v",
			elapsed, budget)
	}
	if elapsed+retry+30*time.Millisecond < budget {
		t.Fatalf("busy returned too early: elapsed=%v retry=%v budget=%v",
			elapsed, retry, budget)
	}
}

func TestSubscriptionBarrierReturnsAProvenFastBusySignal(t *testing.T) {
	const configDigest = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	path := filepath.Join(t.TempDir(), "subscription.lock")
	locker := newFileSubscriptionLocker(path, 100*time.Millisecond)
	owner, err := locker.Acquire(context.Background())
	if err != nil {
		t.Fatalf("owner acquire: %v", err)
	}
	defer owner.Release()
	if err := os.WriteFile(path+".barrier", []byte("v1\n"), 0600); err != nil {
		t.Fatalf("write barrier: %v", err)
	}

	started := time.Now()
	held, err := locker.Acquire(context.Background())
	elapsed := time.Since(started)
	if held != nil {
		_ = held.Release()
		t.Fatal("barrier acquire unexpectedly returned a lock")
	}
	if !errors.Is(err, errSubscriptionBusy) {
		t.Fatalf("barrier acquire error = %v", err)
	}
	if elapsed >= 100*time.Millisecond {
		t.Fatalf("barrier did not fail fast: %v", elapsed)
	}

	url := "https://barrier.invalid/list"
	engine := newSubscriptionAggregateEngine(
		gatewayConfig{
			ConfigDigest:            configDigest,
			SourceTimeoutSeconds:    5,
			AggregateTimeoutSeconds: 2,
			Sources: []gatewaySource{{
				URL: url, URLDigest: testSHA256(url),
			}},
		},
		subscriptionEngineDependencies{
			Locker: locker,
			Store: &scriptedGenerationStore{
				observation: currentObservation{Kind: currentAbsent},
				selection:   currentSelection{Kind: currentAbsent},
			},
			Fetcher: &recordingSourceFetcher{},
			Normalizer: &recordingSourceNormalizer{
				output: []byte(`{"outbounds":[{"type":"socks"}]}`),
			},
		},
	)
	response := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodGet, "/v1/aggregate", nil)
	gatewayHTTPHandler{
		config: gatewayConfig{
			ConfigDigest:            configDigest,
			AggregateTimeoutSeconds: 2,
			Sources: []gatewaySource{{
				URL: url, URLDigest: testSHA256(url),
			}},
		},
		engine: engine,
	}.ServeHTTP(response, request)
	if response.Code != http.StatusServiceUnavailable ||
		!strings.Contains(response.Body.String(), `"code":"busy"`) {
		t.Fatalf("barrier response = %d %s",
			response.Code, response.Body.String())
	}
}

func TestSubscriptionBarrierIgnoresUnsafeMarkers(t *testing.T) {
	cases := []struct {
		name string
		make func(*testing.T, string)
	}{
		{
			name: "wrong content",
			make: func(t *testing.T, path string) {
				if err := os.WriteFile(path, []byte("not-v1\n"), 0600); err != nil {
					t.Fatal(err)
				}
			},
		},
		{
			name: "wrong mode",
			make: func(t *testing.T, path string) {
				if err := os.WriteFile(path, []byte("v1\n"), 0644); err != nil {
					t.Fatal(err)
				}
			},
		},
		{
			name: "symlink",
			make: func(t *testing.T, path string) {
				target := path + ".target"
				if err := os.WriteFile(target, []byte("v1\n"), 0600); err != nil {
					t.Fatal(err)
				}
				if err := os.Symlink(target, path); err != nil {
					t.Fatal(err)
				}
			},
		},
		{
			name: "hard link",
			make: func(t *testing.T, path string) {
				target := path + ".target"
				if err := os.WriteFile(target, []byte("v1\n"), 0600); err != nil {
					t.Fatal(err)
				}
				if err := os.Link(target, path); err != nil {
					t.Fatal(err)
				}
			},
		},
	}

	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "subscription.lock")
			locker := newFileSubscriptionLocker(path, 20*time.Millisecond)
			owner, err := locker.Acquire(context.Background())
			if err != nil {
				t.Fatal(err)
			}
			defer owner.Release()
			testCase.make(t, path+".barrier")

			ctx, cancel := context.WithTimeout(
				context.Background(), 90*time.Millisecond,
			)
			defer cancel()
			held, err := locker.Acquire(ctx)
			if held != nil {
				_ = held.Release()
				t.Fatal("unsafe marker acquired the held lock")
			}
			if !errors.Is(err, errSubscriptionBusy) {
				t.Fatalf("unsafe marker error = %v", err)
			}
		})
	}
}

func TestSubscriptionLockBusySurvivesHTTPDeadlineArbitration(t *testing.T) {
	const configDigest = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	path := filepath.Join(t.TempDir(), "subscription.lock")
	const retry = 500 * time.Millisecond
	locker := newFileSubscriptionLocker(path, retry)
	owner, err := locker.Acquire(context.Background())
	if err != nil {
		t.Fatalf("owner acquire: %v", err)
	}
	defer owner.Release()

	url := "https://busy.invalid/list"
	engine := newSubscriptionAggregateEngine(
		gatewayConfig{
			ConfigDigest:            configDigest,
			SourceTimeoutSeconds:    5,
			AggregateTimeoutSeconds: 2,
			Sources: []gatewaySource{{
				URL: url, URLDigest: testSHA256(url),
			}},
		},
		subscriptionEngineDependencies{
			Locker: locker,
			Store: &scriptedGenerationStore{
				observation: currentObservation{Kind: currentAbsent},
				selection:   currentSelection{Kind: currentAbsent},
			},
			Fetcher: &recordingSourceFetcher{},
			Normalizer: &recordingSourceNormalizer{
				output: []byte(`{"outbounds":[{"type":"socks"}]}`),
			},
		},
	)
	server := httptest.NewServer(gatewayHTTPHandler{
		config: gatewayConfig{
			ConfigDigest:            configDigest,
			AggregateTimeoutSeconds: 2,
			Sources: []gatewaySource{{
				URL: url, URLDigest: testSHA256(url),
			}},
		},
		engine: engine,
	})
	defer server.Close()

	started := time.Now()
	response, err := http.Get(server.URL + "/v1/aggregate")
	elapsed := time.Since(started)
	if err != nil {
		t.Fatalf("aggregate request: %v", err)
	}
	defer response.Body.Close()
	body, err := io.ReadAll(response.Body)
	if err != nil {
		t.Fatal(err)
	}
	if response.StatusCode != http.StatusServiceUnavailable ||
		!strings.Contains(string(body), `"code":"busy"`) {
		t.Fatalf("busy response = %d %s", response.StatusCode, body)
	}
	if elapsed >= 2*time.Second {
		t.Fatalf("HTTP busy lost deadline arbitration: %v", elapsed)
	}
	if elapsed+retry+100*time.Millisecond < 2*time.Second {
		t.Fatalf("HTTP busy returned too early: %v", elapsed)
	}
}

func TestSubscriptionLockRejectsPathReplacementAfterOpen(t *testing.T) {
	directory := t.TempDir()
	path := filepath.Join(directory, "subscription.lock")
	locker := newFileSubscriptionLocker(path, 500*time.Millisecond)
	owner, err := locker.Acquire(context.Background())
	if err != nil {
		t.Fatalf("owner acquire: %v", err)
	}
	opened := make(chan struct{})
	proceed := make(chan struct{})
	locker.afterOpenTestHook = func() {
		close(opened)
		<-proceed
	}

	type acquireResult struct {
		held heldSubscriptionLock
		err  error
	}
	result := make(chan acquireResult, 1)
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	go func() {
		held, acquireErr := locker.Acquire(ctx)
		result <- acquireResult{held: held, err: acquireErr}
	}()

	<-opened
	oldPath := path + ".old"
	if err := os.Rename(path, oldPath); err != nil {
		t.Fatalf("replace rename: %v", err)
	}
	if err := os.WriteFile(path, nil, 0600); err != nil {
		t.Fatalf("replacement create: %v", err)
	}
	close(proceed)
	if err := owner.Release(); err != nil {
		t.Fatalf("owner release: %v", err)
	}

	waiter := <-result
	if waiter.err == nil {
		_ = waiter.held.Release()
		t.Fatal("waiter accepted a replaced lock pathname")
	}
}

func TestSubscriptionLockDoesNotAcquireAfterContextCancelledPostOpen(t *testing.T) {
	path := filepath.Join(t.TempDir(), "subscription.lock")
	ctx, cancel := context.WithCancel(context.Background())
	locker := newFileSubscriptionLocker(path, 10*time.Millisecond)
	locker.afterOpenTestHook = cancel
	held, err := locker.Acquire(ctx)
	if err == nil {
		_ = held.Release()
		t.Fatal("free lock was acquired after cancellation")
	}

	locker.afterOpenTestHook = nil
	reacquired, err := locker.Acquire(context.Background())
	if err != nil {
		t.Fatalf("cancelled acquire retained lock: %v", err)
	}
	if err := reacquired.Release(); err != nil {
		t.Fatalf("release: %v", err)
	}
}

func TestSubscriptionLockRejectsUnsafeTargets(t *testing.T) {
	cases := []struct {
		name string
		make func(*testing.T, string)
	}{
		{
			name: "symlink",
			make: func(t *testing.T, path string) {
				target := path + ".target"
				if err := os.WriteFile(target, []byte("target"), 0600); err != nil {
					t.Fatal(err)
				}
				if err := os.Symlink(target, path); err != nil {
					t.Fatal(err)
				}
			},
		},
		{
			name: "directory",
			make: func(t *testing.T, path string) {
				if err := os.Mkdir(path, 0700); err != nil {
					t.Fatal(err)
				}
			},
		},
		{
			name: "fifo",
			make: func(t *testing.T, path string) {
				if err := syscall.Mkfifo(path, 0600); err != nil {
					t.Fatal(err)
				}
			},
		},
		{
			name: "hard link",
			make: func(t *testing.T, path string) {
				original := path + ".original"
				if err := os.WriteFile(original, nil, 0600); err != nil {
					t.Fatal(err)
				}
				if err := os.Link(original, path); err != nil {
					t.Fatal(err)
				}
			},
		},
		{
			name: "mode 0644",
			make: func(t *testing.T, path string) {
				if err := os.WriteFile(path, nil, 0644); err != nil {
					t.Fatal(err)
				}
			},
		},
		{
			name: "mode 0400",
			make: func(t *testing.T, path string) {
				if err := os.WriteFile(path, nil, 0400); err != nil {
					t.Fatal(err)
				}
			},
		},
	}

	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "subscription.lock")
			testCase.make(t, path)
			before, err := os.Lstat(path)
			if err != nil {
				t.Fatal(err)
			}
			ctx, cancel := context.WithTimeout(
				context.Background(), 500*time.Millisecond,
			)
			defer cancel()
			held, err := newFileSubscriptionLocker(
				path, 10*time.Millisecond,
			).Acquire(ctx)
			if err == nil {
				_ = held.Release()
				t.Fatal("unsafe lock target accepted")
			}
			after, statErr := os.Lstat(path)
			if statErr != nil {
				t.Fatalf("unsafe target was removed: %v", statErr)
			}
			if before.Mode() != after.Mode() {
				t.Fatalf("unsafe target mode changed from %v to %v",
					before.Mode(), after.Mode())
			}
		})
	}
}
