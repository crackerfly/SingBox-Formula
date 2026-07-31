package main

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"
)

func TestCommitGateDeadlineDeniesLaterLogicalCommit(t *testing.T) {
	store := newGateTestStore(false)
	server := newGateTestServer(t, store)
	defer server.Close()

	responseDone := requestGateAggregate(server.URL)
	awaitGateSignal(t, store.commitEntered, "commit entry")
	response := awaitGateResponse(t, responseDone)
	if response.status != http.StatusInternalServerError {
		t.Fatalf("deadline response = %d %s", response.status, response.body)
	}
	close(store.releaseCommit)
	select {
	case <-store.commitFinished:
	case <-time.After(2 * time.Second):
		t.Fatal("staging commit did not finish")
	}
	if store.beginSucceeded || store.publishCount != 0 {
		t.Fatalf("late Begin=%v publishes=%d",
			store.beginSucceeded, store.publishCount)
	}
}

func TestCommitGateBeginBeforeDeadlineWaitsForCommittedResult(t *testing.T) {
	store := newGateTestStore(true)
	server := newGateTestServer(t, store)
	defer server.Close()

	responseDone := requestGateAggregate(server.URL)
	awaitGateSignal(t, store.beginCalled, "commit Begin")
	select {
	case response := <-responseDone:
		t.Fatalf("handler returned after Begin before result: %d %s",
			response.status, response.body)
	case <-time.After(1200 * time.Millisecond):
	}
	close(store.releaseCommit)
	response := awaitGateResponse(t, responseDone)
	if response.status != http.StatusOK ||
		response.body != `{"gate":"committed"}` {
		t.Fatalf("committed response = %d %s",
			response.status, response.body)
	}
	if !store.beginSucceeded || store.publishCount != 1 {
		t.Fatalf("Begin=%v publishes=%d",
			store.beginSucceeded, store.publishCount)
	}
}

type gateHTTPResponse struct {
	status int
	body   string
}

func requestGateAggregate(serverURL string) <-chan gateHTTPResponse {
	done := make(chan gateHTTPResponse, 1)
	go func() {
		response, err := http.Get(serverURL + "/v1/aggregate")
		if err != nil {
			done <- gateHTTPResponse{body: err.Error()}
			return
		}
		defer response.Body.Close()
		body, _ := io.ReadAll(response.Body)
		done <- gateHTTPResponse{
			status: response.StatusCode,
			body:   string(body),
		}
	}()
	return done
}

func awaitGateResponse(
	t *testing.T,
	response <-chan gateHTTPResponse,
) gateHTTPResponse {
	t.Helper()
	select {
	case result := <-response:
		return result
	case <-time.After(6 * time.Second):
		t.Fatal("aggregate HTTP response exceeded deterministic test bound")
		return gateHTTPResponse{}
	}
}

func awaitGateSignal(
	t *testing.T,
	signal <-chan struct{},
	name string,
) {
	t.Helper()
	select {
	case <-signal:
	case <-time.After(6 * time.Second):
		t.Fatalf("%s exceeded deterministic test bound", name)
	}
}

const gateTestConfigDigest = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"

type gateTestStore struct {
	beginBeforeWait bool
	commitEntered   chan struct{}
	beginCalled     chan struct{}
	releaseCommit   chan struct{}
	commitFinished  chan struct{}
	beginSucceeded  bool
	publishCount    int
	finishOnce      sync.Once
}

func newGateTestStore(beginBeforeWait bool) *gateTestStore {
	return &gateTestStore{
		beginBeforeWait: beginBeforeWait,
		commitEntered:   make(chan struct{}),
		beginCalled:     make(chan struct{}),
		releaseCommit:   make(chan struct{}),
		commitFinished:  make(chan struct{}),
	}
}

func (store *gateTestStore) ObserveCurrent(
	context.Context,
) (currentObservation, error) {
	return currentObservation{Kind: currentAbsent}, nil
}

func (store *gateTestStore) LoadCurrent(
	context.Context,
) (currentSelection, error) {
	return currentSelection{Kind: currentAbsent}, nil
}

func (store *gateTestStore) Commit(
	ctx context.Context,
	_ generationCandidate,
) (generationCommitResult, error) {
	close(store.commitEntered)
	defer store.finishOnce.Do(func() { close(store.commitFinished) })
	if store.beginBeforeWait {
		store.beginSucceeded = beginCurrentCommit(ctx)
		close(store.beginCalled)
		<-store.releaseCommit
	} else {
		<-store.releaseCommit
		store.beginSucceeded = beginCurrentCommit(ctx)
		close(store.beginCalled)
	}
	if !store.beginSucceeded {
		return generationCommitResult{}, nil
	}
	store.publishCount++
	return generationCommitResult{
		Committed: true,
		Selection: currentSelection{
			Kind: currentPresent,
			Generation: validatedGeneration{
				GenerationID: "1111111111111111111111111111111111111111111111111111111111111111",
				ConfigDigest: gateTestConfigDigest,
				Aggregate:    []byte(`{"gate":"committed"}`),
			},
		},
	}, nil
}

func newGateTestServer(
	t *testing.T,
	store generationStore,
) *httptest.Server {
	t.Helper()
	url := "https://gate.invalid/list"
	config := gatewayConfig{
		ConfigDigest:            gateTestConfigDigest,
		SourceTimeoutSeconds:    5,
		AggregateTimeoutSeconds: 1,
		WriteTimeoutSeconds:     3,
		Sources: []gatewaySource{{
			URL: url, URLDigest: testSHA256(url),
		}},
	}
	engine := newSubscriptionAggregateEngine(
		config,
		subscriptionEngineDependencies{
			Locker: &recordingSubscriptionLocker{},
			Store:  store,
			Fetcher: &recordingSourceFetcher{
				results: map[string]sourceFetchResult{
					url: {Body: []byte("fresh"), Code: fetchCodeOK},
				},
			},
			Normalizer: &recordingSourceNormalizer{
				output: []byte(`{"outbounds":[{"type":"socks"}]}`),
				info: NormalizeInfo{
					Format: FormatSingBoxJSON, Accepted: 1,
				},
			},
		},
	)
	return httptest.NewServer(gatewayHTTPHandler{
		config: config,
		engine: engine,
	})
}
