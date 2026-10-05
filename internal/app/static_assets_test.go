package app

import (
	"io/fs"
	"net/http"
	"net/http/httptest"
	"regexp"
	"strings"
	"testing"
)

// The frontend must not depend on any third-party origin at runtime, so every
// script and stylesheet a template references has to be embedded in the binary.
func TestTemplatesReferenceOnlyEmbeddedAssets(t *testing.T) {
	t.Parallel()

	assetRef := regexp.MustCompile(`(?:src|href)="(/static/[^"]+)"`)
	externalRef := regexp.MustCompile(`(?:src|href)="(?:https?:)?//`)

	templates, err := fs.Glob(templatesFS, "templates/*.html")
	if err != nil {
		t.Fatal(err)
	}

	for _, name := range templates {
		content, err := fs.ReadFile(templatesFS, name)
		if err != nil {
			t.Fatal(err)
		}
		if externalRef.Match(content) {
			t.Errorf("%s loads an asset from another origin", name)
		}
		for _, match := range assetRef.FindAllSubmatch(content, -1) {
			embeddedPath := strings.TrimPrefix(string(match[1]), "/")
			if _, err := fs.Stat(staticFS, embeddedPath); err != nil {
				t.Errorf("%s references %s, which is not embedded (run make vendor)", name, match[1])
			}
		}
	}
}

func TestVendoredAssetsAreServedWithStrictPolicy(t *testing.T) {
	t.Parallel()

	server, database, _ := newCompileTargetTestServer(t)
	defer database.Close()

	for _, path := range []string{
		"/static/vendor/monaco-editor-0.45.0/vs/loader.js",
		"/static/vendor/monaco-editor-0.45.0/vs/editor/editor.main.js",
		"/static/vendor/monaco-editor-0.45.0/vs/base/worker/workerMain.js",
		"/static/vendor/pdfjs-dist-3.11.174/pdf.min.js",
		"/static/vendor/pdfjs-dist-3.11.174/pdf.worker.min.js",
	} {
		rec := httptest.NewRecorder()
		server.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, path, nil))

		if rec.Code != http.StatusOK {
			t.Errorf("GET %s status=%d", path, rec.Code)
			continue
		}
		if got := rec.Header().Get("Content-Security-Policy"); got != contentSecurityPolicy {
			t.Errorf("GET %s Content-Security-Policy=%q", path, got)
		}
		if got := rec.Header().Get("Cache-Control"); !strings.Contains(got, "immutable") {
			t.Errorf("GET %s Cache-Control=%q, want immutable", path, got)
		}
	}

	rec := httptest.NewRecorder()
	server.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/static/editor.js", nil))
	if got := rec.Header().Get("Cache-Control"); got != "" {
		t.Errorf("unversioned /static/editor.js Cache-Control=%q, want none", got)
	}
}

func TestPagesSendStrictPolicy(t *testing.T) {
	t.Parallel()

	server, database, _ := newCompileTargetTestServer(t)
	defer database.Close()

	rec := httptest.NewRecorder()
	server.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/login", nil))

	policy := rec.Header().Get("Content-Security-Policy")
	for _, directive := range []string{"script-src 'self';", "style-src 'self';", "object-src 'none'"} {
		if !strings.Contains(policy, directive) {
			t.Errorf("login Content-Security-Policy=%q, missing %q", policy, directive)
		}
	}
	if strings.Contains(policy, "unsafe-") {
		t.Errorf("login Content-Security-Policy=%q allows unsafe sources", policy)
	}
}
