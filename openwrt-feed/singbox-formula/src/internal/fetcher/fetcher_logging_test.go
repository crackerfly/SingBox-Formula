package fetcher

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"go.uber.org/zap"
	"go.uber.org/zap/zapcore"
	"go.uber.org/zap/zaptest/observer"
)

func TestFetchLogRetainsCompleteSubscriptionAndCacheBusterURL(t *testing.T) {
	requested := make(chan string, 1)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requested <- r.URL.String()
		_, _ = w.Write([]byte(`{"outbounds":[{"tag":"node"}]}`))
	}))
	defer server.Close()

	core, observed := observer.New(zapcore.DebugLevel)
	client := &Client{http: server.Client(), logger: zap.New(core)}
	_, actualURL, err := client.FetchBytes(
		context.Background(),
		server.URL+"/nodes?token=full-subscription-token",
		NodeResponseLimit,
	)
	if err != nil {
		t.Fatalf("FetchBytes() error = %v", err)
	}
	requestURL := <-requested
	if !strings.Contains(requestURL, "token=full-subscription-token") || !strings.Contains(requestURL, "_t=") || !strings.Contains(requestURL, "_r=") {
		t.Fatalf("observed request URL incomplete: %q", requestURL)
	}
	if !strings.Contains(actualURL, "token=full-subscription-token") || !strings.Contains(actualURL, "&_t=") || !strings.Contains(actualURL, "&_r=") {
		t.Fatalf("returned URL incomplete: %q", actualURL)
	}
	found := false
	for _, entry := range observed.All() {
		if strings.Contains(entry.Message, actualURL) {
			found = true
		}
	}
	if !found {
		t.Fatalf("complete actual URL %q missing from debug logs: %+v", actualURL, observed.All())
	}
}
