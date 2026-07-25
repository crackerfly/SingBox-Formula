package handler

import (
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"

	"github.com/haierkeys/singbox-subscribe-convert/global"
	"go.uber.org/zap"
)

func TestUnauthorizedRequestRetainsCompleteQueryAndAuthValues(t *testing.T) {
	oldConfig, oldLogger := cfg, logger
	cfg = &global.Config{Auth: global.AuthConfig{Password: "expected-secret"}}
	logger = zap.NewNop()
	t.Cleanup(func() {
		cfg, logger = oldConfig, oldLogger
	})

	output := captureStdout(t, func() {
		request := httptest.NewRequest(http.MethodGet, "/?password=provided-secret&type=momo&_t=123&token=full-token", nil)
		response := httptest.NewRecorder()
		HandleRequest(response, request)
		if response.Code != http.StatusUnauthorized {
			t.Fatalf("status = %d, want 401", response.Code)
		}
	})
	if !strings.Contains(output, "/?password=provided-secret&type=momo&_t=123&token=full-token") {
		t.Fatalf("complete query missing from output:\n%s", output)
	}
	if !strings.Contains(output, "expected 'expected-secret', got 'provided-secret'") {
		t.Fatalf("complete auth values missing from output:\n%s", output)
	}
}

func captureStdout(t *testing.T, action func()) string {
	t.Helper()
	reader, writer, err := os.Pipe()
	if err != nil {
		t.Fatalf("create stdout pipe: %v", err)
	}
	original := os.Stdout
	os.Stdout = writer
	defer func() { os.Stdout = original }()
	action()
	_ = writer.Close()
	data, err := io.ReadAll(reader)
	_ = reader.Close()
	if err != nil {
		t.Fatalf("read stdout: %v", err)
	}
	return string(data)
}
