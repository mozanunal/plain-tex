package app

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// The editor injects the saved entry point into JavaScript. html/template
// already emits a quoted, escaped JS string literal in a script context, so
// wrapping the value in printf %q first produced "\"main.tex\"", a string whose
// contents included quote characters. That never matched a real path, so the
// stored entry point was silently ignored on every reload.
func TestEditorRendersCompileTargetAsPlainJSString(t *testing.T) {
	t.Parallel()

	server, database, projectsDir := newCompileTargetTestServer(t)
	defer database.Close()

	const userID = "user-1"
	const projectID = "project-1"

	insertTestUser(t, database, userID)
	insertTestProject(t, database, projectID, userID)
	writeCompileTestFile(t, projectsDir+"/"+projectID+"/main.tex", `\documentclass{article}`)

	if err := server.setProjectCompileEntry(projectID, "main.tex"); err != nil {
		t.Fatalf("setProjectCompileEntry: %v", err)
	}

	req := httptest.NewRequest(http.MethodGet, "/editor/"+projectID, nil)
	req = req.WithContext(withRouteUserContext(req.Context(), projectID, &User{ID: userID, Email: "owner@example.com"}))

	rec := httptest.NewRecorder()
	server.handleEditorPage(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("handleEditorPage status=%d", rec.Code)
	}

	body := rec.Body.String()
	const want = `const initialCompileTarget = "main.tex";`
	if !strings.Contains(body, want) {
		for _, line := range strings.Split(body, "\n") {
			if strings.Contains(line, "initialCompileTarget =") {
				t.Fatalf("rendered %q, want %q", strings.TrimSpace(line), want)
			}
		}
		t.Fatalf("editor page does not define initialCompileTarget at all")
	}
}
