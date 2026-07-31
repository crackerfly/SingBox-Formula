package handler

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/flosch/pongo2/v6"
	"github.com/haierkeys/singbox-subscribe-convert/global"
)

type healthResponse struct {
	Service     string `json:"service"`
	Version     string `json:"version"`
	Status      string `json:"status"`
	HasData     bool   `json:"has_data"`
	HasTemplate bool   `json:"has_template"`
	NodeCount   int    `json:"node_count"`
	TemplateCnt int    `json:"template_count"`
}

func TestHealthReturnsStableServiceIdentityAndVersion(t *testing.T) {
	if global.Version != "0.7.2-formula" {
		t.Fatalf("built-in converter version = %q, want forked 0.7.2-formula", global.Version)
	}

	setHealthState(t, []map[string]interface{}{{"tag": "node"}}, map[string]*pongo2.Template{"momo": nil})
	recorder := httptest.NewRecorder()
	HandleHealth(recorder, httptest.NewRequest(http.MethodGet, "/health", nil))
	if recorder.Code != http.StatusOK {
		t.Fatalf("healthy status code = %d, want 200", recorder.Code)
	}
	response := decodeHealth(t, recorder)
	if response.Service != "singbox-subscribe-convert" {
		t.Errorf("service = %q", response.Service)
	}
	if response.Version != global.Version {
		t.Errorf("version = %q, want %q", response.Version, global.Version)
	}
	if response.Status != "ok" || !response.HasData || !response.HasTemplate || response.NodeCount != 1 || response.TemplateCnt != 1 {
		t.Errorf("healthy response = %+v", response)
	}
}

func TestHealthRetainsDegradedStatusAndCounts(t *testing.T) {
	setHealthState(t, nil, nil)
	recorder := httptest.NewRecorder()
	HandleHealth(recorder, httptest.NewRequest(http.MethodGet, "/health", nil))
	if recorder.Code != http.StatusServiceUnavailable {
		t.Fatalf("degraded status code = %d, want 503", recorder.Code)
	}
	response := decodeHealth(t, recorder)
	if response.Service != "singbox-subscribe-convert" || response.Status != "degraded" || response.HasData || response.HasTemplate {
		t.Errorf("degraded response = %+v", response)
	}
}

func setHealthState(t *testing.T, data []map[string]interface{}, templateState map[string]*pongo2.Template) {
	t.Helper()
	oldSnapshot := getSnapshot()
	applySnapshot(&dataSnapshot{nodeData: data, templates: templateState})
	t.Cleanup(func() {
		applySnapshot(oldSnapshot)
	})
}

func decodeHealth(t *testing.T, recorder *httptest.ResponseRecorder) healthResponse {
	t.Helper()
	var response healthResponse
	if err := json.Unmarshal(recorder.Body.Bytes(), &response); err != nil {
		t.Fatalf("decode health response %q: %v", recorder.Body.String(), err)
	}
	return response
}
