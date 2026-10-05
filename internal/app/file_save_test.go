package app

import (
	"net/http"
	"net/http/httptest"
	"net/url"
	"path/filepath"
	"strings"
	"testing"

	"github.com/go-chi/chi/v5"
)

func fileRequest(t *testing.T, method, projectID, userID, rel, body string) *http.Request {
	t.Helper()
	req := httptest.NewRequest(method, "/api/projects/"+projectID+"/files/"+rel, strings.NewReader(body))
	if body != "" {
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	}
	ctx := withRouteUserContext(req.Context(), projectID, &User{ID: userID, Email: "owner@example.com"})
	chi.RouteContext(ctx).URLParams.Add("*", rel)
	return req.WithContext(ctx)
}

func TestUpdateFileRejectsStaleBaseHash(t *testing.T) {
	t.Parallel()
	server, database, projectsDir := newCompileTargetTestServer(t)
	defer database.Close()

	const userID, projectID = "user-1", "project-1"
	insertTestUser(t, database, userID)
	insertTestProject(t, database, projectID, userID)
	writeCompileTestFile(t, filepath.Join(projectsDir, projectID, "main.tex"), "original")

	// Read the current ETag.
	getRec := httptest.NewRecorder()
	server.handleGetFile(getRec, fileRequest(t, http.MethodGet, projectID, userID, "main.tex", ""))
	etag := getRec.Header().Get("ETag")
	if etag == "" {
		t.Fatal("expected an ETag on file read")
	}

	// A save with the matching base hash succeeds.
	okRec := httptest.NewRecorder()
	server.handleUpdateFile(okRec, fileRequest(t, http.MethodPut, projectID, userID, "main.tex",
		url.Values{"content": {"v2"}, "baseHash": {etag}}.Encode()))
	if okRec.Code != http.StatusOK {
		t.Fatalf("matching save status=%d want 200", okRec.Code)
	}

	// A second save with the now-stale base hash is rejected with 409.
	staleRec := httptest.NewRecorder()
	server.handleUpdateFile(staleRec, fileRequest(t, http.MethodPut, projectID, userID, "main.tex",
		url.Values{"content": {"v3"}, "baseHash": {etag}}.Encode()))
	if staleRec.Code != http.StatusConflict {
		t.Fatalf("stale save status=%d want 409", staleRec.Code)
	}

	// A save without a base hash still works (backwards compatible).
	noHashRec := httptest.NewRecorder()
	server.handleUpdateFile(noHashRec, fileRequest(t, http.MethodPut, projectID, userID, "main.tex",
		url.Values{"content": {"v4"}}.Encode()))
	if noHashRec.Code != http.StatusOK {
		t.Fatalf("no-base-hash save status=%d want 200", noHashRec.Code)
	}
}

func TestCompiledOutputGoesToHiddenBuildDir(t *testing.T) {
	t.Parallel()
	if CompiledPDFRelPath != ".polytex-build/main.pdf" {
		t.Fatalf("unexpected compiled PDF path %q", CompiledPDFRelPath)
	}
	if !hasHiddenSegment(CompiledPDFRelPath) {
		t.Fatal("build dir must be hidden so the file browser and git skip it")
	}
}
