package httpserver

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestLiveness(t *testing.T) {
	request := httptest.NewRequest(http.MethodGet, "/health/live", nil)
	result := httptest.NewRecorder()
	New().ServeHTTP(result, request)
	if result.Code != http.StatusOK {
		t.Fatalf("status = %d", result.Code)
	}
	if result.Body.String() != "{\"status\":\"ok\"}\n" {
		t.Fatalf("body = %q", result.Body.String())
	}
}
func TestNoUnprotectedBusinessAPIs(t *testing.T) {
	req := httptest.NewRequest(http.MethodGet, "/v1/contractors", nil)
	resp := httptest.NewRecorder()
	New().ServeHTTP(resp, req)
	if resp.Code != http.StatusNotFound {
		t.Fatalf("unimplemented endpoint exposed: %d", resp.Code)
	}
}
