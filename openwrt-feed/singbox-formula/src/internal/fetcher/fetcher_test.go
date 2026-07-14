package fetcher

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"go.uber.org/zap"
)

func TestNodeResponseLimitRejectsThirtyTwoMiBPlusOne(t *testing.T) {
	server := repeatedResponseServer(t, NodeResponseLimit+1)
	defer server.Close()

	client := testClient(server)
	data, actualURL, err := client.FetchBytes(context.Background(), server.URL+"/nodes?token=node-secret", NodeResponseLimit)
	if !errors.Is(err, ErrResponseTooLarge) {
		t.Fatalf("FetchBytes() error = %v, want ErrResponseTooLarge", err)
	}
	if data != nil {
		t.Fatalf("FetchBytes() returned %d bytes after overflow, want nil", len(data))
	}
	if !strings.Contains(actualURL, "token=node-secret") || !strings.Contains(actualURL, "&_t=") || !strings.Contains(actualURL, "&_r=") {
		t.Fatalf("actual URL lost full query/cache buster: %q", actualURL)
	}
}

func TestTemplateResponseLimitRejectsEightMiBPlusOne(t *testing.T) {
	server := repeatedResponseServer(t, TemplateResponseLimit+1)
	defer server.Close()

	data, _, err := testClient(server).FetchBytes(context.Background(), server.URL, TemplateResponseLimit)
	if !errors.Is(err, ErrResponseTooLarge) {
		t.Fatalf("FetchBytes() error = %v, want ErrResponseTooLarge", err)
	}
	if data != nil {
		t.Fatalf("FetchBytes() returned %d bytes after overflow, want nil", len(data))
	}
}

func TestResponseLimitAcceptsExactBoundary(t *testing.T) {
	server := repeatedResponseServer(t, TemplateResponseLimit)
	defer server.Close()

	data, _, err := testClient(server).FetchBytes(context.Background(), server.URL, TemplateResponseLimit)
	if err != nil {
		t.Fatalf("FetchBytes() exact boundary error = %v", err)
	}
	if got := int64(len(data)); got != TemplateResponseLimit {
		t.Fatalf("FetchBytes() length = %d, want %d", got, TemplateResponseLimit)
	}
}

func TestFetchBytesHonorsRequestContext(t *testing.T) {
	requestStarted := make(chan struct{})
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		close(requestStarted)
		<-r.Context().Done()
	}))
	defer server.Close()

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() {
		_, _, err := testClient(server).FetchBytes(ctx, server.URL, 1024)
		done <- err
	}()
	<-requestStarted
	cancel()
	if err := <-done; !errors.Is(err, context.Canceled) {
		t.Fatalf("FetchBytes() cancellation error = %v, want context.Canceled", err)
	}
}

func repeatedResponseServer(t *testing.T, size int64) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/octet-stream")
		// Flush before writing so net/http uses chunked transfer encoding and the
		// body limit is enforced independently of Content-Length.
		if flusher, ok := w.(http.Flusher); ok {
			flusher.Flush()
		}
		if _, err := io.CopyN(w, repeatingReader{}, size); err != nil {
			t.Errorf("write response: %v", err)
		}
	}))
}

func testClient(server *httptest.Server) *Client {
	return &Client{http: server.Client(), logger: zap.NewNop()}
}

type repeatingReader struct{}

func (repeatingReader) Read(buffer []byte) (int, error) {
	for i := range buffer {
		buffer[i] = 'x'
	}
	return len(buffer), nil
}
