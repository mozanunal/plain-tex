package app

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestHealthEndpointReportsOK(t *testing.T) {
	t.Parallel()

	server, database, _ := newCompileTargetTestServer(t)
	defer database.Close()

	recorder := httptest.NewRecorder()
	server.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, healthPath, nil))

	if recorder.Code != http.StatusOK {
		t.Fatalf("health status=%d want %d", recorder.Code, http.StatusOK)
	}
	if contentType := recorder.Header().Get("Content-Type"); contentType != "application/json" {
		t.Fatalf("health content type=%q want application/json", contentType)
	}

	var payload map[string]string
	if err := json.Unmarshal(recorder.Body.Bytes(), &payload); err != nil {
		t.Fatalf("health body is not valid JSON: %v (body=%q)", err, recorder.Body.String())
	}
	if payload["status"] != "ok" {
		t.Fatalf("health status field=%q want %q", payload["status"], "ok")
	}
}

func TestHealthEndpointReportsUnavailableWhenDatabaseIsDown(t *testing.T) {
	t.Parallel()

	server, database, _ := newCompileTargetTestServer(t)
	database.Close()

	recorder := httptest.NewRecorder()
	server.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, healthPath, nil))

	if recorder.Code != http.StatusServiceUnavailable {
		t.Fatalf("health status=%d want %d", recorder.Code, http.StatusServiceUnavailable)
	}

	var payload map[string]string
	if err := json.Unmarshal(recorder.Body.Bytes(), &payload); err != nil {
		t.Fatalf("health body is not valid JSON: %v", err)
	}
	if payload["status"] != "unavailable" {
		t.Fatalf("health status field=%q want %q", payload["status"], "unavailable")
	}
}
