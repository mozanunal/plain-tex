package app

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/go-chi/chi/v5"
)

func TestResolveProjectPathRejectsSymlinkEscapes(t *testing.T) {
	t.Parallel()

	baseDir := t.TempDir()
	projectDir := filepath.Join(baseDir, "project")
	outsideDir := filepath.Join(baseDir, "outside")
	writeCompileTestFile(t, filepath.Join(projectDir, "main.tex"), "inside")
	writeCompileTestFile(t, filepath.Join(projectDir, "figures", "plot.txt"), "inside")
	writeCompileTestFile(t, filepath.Join(outsideDir, "secret.txt"), "secret")

	mustSymlink(t, filepath.Join(outsideDir, "secret.txt"), filepath.Join(projectDir, "leak.txt"))
	mustSymlink(t, outsideDir, filepath.Join(projectDir, "outside-dir"))
	mustSymlink(t, filepath.Join(outsideDir, "missing.txt"), filepath.Join(projectDir, "dangling.txt"))
	mustSymlink(t, filepath.Join(projectDir, "figures"), filepath.Join(projectDir, "figs"))

	rejected := []string{"leak.txt", "outside-dir/secret.txt", "outside-dir/new.txt", "dangling.txt"}
	for _, input := range rejected {
		if _, _, err := resolveProjectPath(projectDir, input, false); err == nil {
			t.Errorf("resolveProjectPath(%q) allowed a path outside the project", input)
		}
	}

	accepted := []string{"main.tex", "figures/plot.txt", "figs/plot.txt", "new/file.tex"}
	for _, input := range accepted {
		if _, _, err := resolveProjectPath(projectDir, input, false); err != nil {
			t.Errorf("resolveProjectPath(%q) returned error: %v", input, err)
		}
	}

	if _, _, err := resolveProjectPathNoFollow(projectDir, "leak.txt", false); err != nil {
		t.Errorf("resolveProjectPathNoFollow should allow removing an escaping symlink itself: %v", err)
	}
	if _, _, err := resolveProjectPathNoFollow(projectDir, "outside-dir/secret.txt", false); err == nil {
		t.Errorf("resolveProjectPathNoFollow allowed a path whose parent is outside the project")
	}
}

func TestNormalizeProjectPathRejectsGitMetadata(t *testing.T) {
	t.Parallel()

	for _, input := range []string{".git/config", ".GIT/hooks/pre-commit", "sub/.git/config", ".git"} {
		if _, err := normalizeProjectPath(input, false); err == nil {
			t.Errorf("normalizeProjectPath(%q) allowed repository metadata", input)
		}
	}
	if _, err := normalizeProjectPath(".gitignore", false); err != nil {
		t.Errorf("normalizeProjectPath(.gitignore) returned error: %v", err)
	}
}

func TestHandleGetFileServesFilesAsInertAttachments(t *testing.T) {
	t.Parallel()

	server, database, projectsDir := newCompileTargetTestServer(t)
	defer database.Close()

	const userID = "user-1"
	const projectID = "project-1"
	insertTestUser(t, database, userID)
	insertTestProject(t, database, projectID, userID)
	writeCompileTestFile(t, filepath.Join(projectsDir, projectID, "evil.html"), "<script>alert(1)</script>")

	rec := getProjectFile(t, server, projectID, userID, "evil.html")
	if rec.Code != http.StatusOK {
		t.Fatalf("status=%d body=%q", rec.Code, rec.Body.String())
	}
	if got := rec.Header().Get("Content-Disposition"); !strings.HasPrefix(got, "attachment") {
		t.Errorf("Content-Disposition=%q, want attachment", got)
	}
	if got := rec.Header().Get("Content-Security-Policy"); !strings.Contains(got, "sandbox") {
		t.Errorf("Content-Security-Policy=%q, want sandbox", got)
	}
	if got := rec.Header().Get("X-Content-Type-Options"); got != "nosniff" {
		t.Errorf("X-Content-Type-Options=%q, want nosniff", got)
	}
}

func TestHandleGetFileRefusesSymlinkOutsideProject(t *testing.T) {
	t.Parallel()

	server, database, projectsDir := newCompileTargetTestServer(t)
	defer database.Close()

	const userID = "user-1"
	const projectID = "project-1"
	insertTestUser(t, database, userID)
	insertTestProject(t, database, projectID, userID)

	secretPath := filepath.Join(filepath.Dir(projectsDir), "test.db")
	writeCompileTestFile(t, filepath.Join(projectsDir, projectID, "main.tex"), "x")
	mustSymlink(t, secretPath, filepath.Join(projectsDir, projectID, "db.txt"))

	rec := getProjectFile(t, server, projectID, userID, "db.txt")
	if rec.Code == http.StatusOK {
		t.Fatalf("symlink to a file outside the project was served")
	}
}

func getProjectFile(t *testing.T, server *Server, projectID, userID, filename string) *httptest.ResponseRecorder {
	t.Helper()

	req := httptest.NewRequest(http.MethodGet, "/api/projects/"+projectID+"/files/"+filename, nil)
	ctx := withRouteUserContext(req.Context(), projectID, &User{ID: userID, Email: "owner@example.com"})
	chi.RouteContext(ctx).URLParams.Add("*", filename)
	rec := httptest.NewRecorder()
	server.handleGetFile(rec, req.WithContext(ctx))
	return rec
}

func mustSymlink(t *testing.T, target, link string) {
	t.Helper()

	if err := os.MkdirAll(filepath.Dir(link), 0755); err != nil {
		t.Fatalf("failed to create parent dir: %v", err)
	}
	if err := os.Symlink(target, link); err != nil {
		t.Fatalf("failed to create symlink: %v", err)
	}
}
