package app

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"regexp"
	"strings"
	"testing"
)

var editorConfigPattern = regexp.MustCompile(`(?s)<script type="application/json" id="editor-config">(.*?)</script>`)

type renderedEditorConfig struct {
	ProjectID     string `json:"projectId"`
	CanWrite      bool   `json:"canWrite"`
	Content       string `json:"content"`
	CompileTarget string `json:"compileTarget"`
}

func renderEditorPage(t *testing.T, content string) *httptest.ResponseRecorder {
	t.Helper()

	server, database, projectsDir := newCompileTargetTestServer(t)
	t.Cleanup(func() { database.Close() })

	const userID = "user-1"
	const projectID = "project-1"

	insertTestUser(t, database, userID)
	insertTestProject(t, database, projectID, userID)
	writeCompileTestFile(t, projectsDir+"/"+projectID+"/main.tex", content)

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
	return rec
}

func parseEditorConfig(t *testing.T, body string) renderedEditorConfig {
	t.Helper()

	match := editorConfigPattern.FindStringSubmatch(body)
	if match == nil {
		t.Fatalf("editor page has no #editor-config block")
	}

	var config renderedEditorConfig
	if err := json.Unmarshal([]byte(match[1]), &config); err != nil {
		t.Fatalf("editor config is not valid JSON: %v\n%s", err, match[1])
	}
	return config
}

// The editor once wrapped the saved entry point in printf %q before handing it
// to html/template, which produced "\"main.tex\"". That never matched a real
// path, so the stored entry point was silently ignored on every reload.
func TestEditorRendersCompileTargetAsPlainString(t *testing.T) {
	t.Parallel()

	rec := renderEditorPage(t, `\documentclass{article}`)
	config := parseEditorConfig(t, rec.Body.String())

	if config.CompileTarget != "main.tex" {
		t.Fatalf("compileTarget=%q, want %q", config.CompileTarget, "main.tex")
	}
	if config.ProjectID != "project-1" || !config.CanWrite {
		t.Fatalf("unexpected config: %+v", config)
	}
}

// File content lands inside a <script> element, so a document containing
// "</script>" must not be able to close it and inject markup.
func TestEditorConfigSurvivesScriptBreakingContent(t *testing.T) {
	t.Parallel()

	const hostile = `</script><script>alert(1)</script> & 'quotes' "double"`
	rec := renderEditorPage(t, hostile)
	body := rec.Body.String()

	if strings.Contains(body, "<script>alert(1)") {
		t.Fatalf("file content escaped the editor config block")
	}
	if got := parseEditorConfig(t, body).Content; got != hostile {
		t.Fatalf("content=%q, want %q", got, hostile)
	}
}

func TestEditorPageHasNoInlineScript(t *testing.T) {
	t.Parallel()

	rec := renderEditorPage(t, `\documentclass{article}`)
	body := rec.Body.String()

	scriptTags := regexp.MustCompile(`<script[^>]*>`).FindAllString(body, -1)
	for _, tag := range scriptTags {
		if strings.Contains(tag, `type="application/json"`) {
			continue
		}
		if !strings.Contains(tag, `src="/static/`) {
			t.Errorf("editor page has an inline or third-party script: %s", tag)
		}
	}

	policy := rec.Header().Get("Content-Security-Policy")
	if policy != editorContentSecurityPolicy {
		t.Errorf("Content-Security-Policy=%q, want the editor policy", policy)
	}
}
