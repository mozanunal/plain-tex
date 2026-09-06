package app

import (
	"context"
	"database/sql"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/go-chi/chi/v5"
	dbpkg "github.com/mozanunal/poly-txt/internal/db"
)

func TestPreferredCompileEntryUsesStoredValue(t *testing.T) {
	t.Parallel()

	server, database, projectsDir := newCompileTargetTestServer(t)
	defer database.Close()

	const userID = "user-1"
	const projectID = "project-1"

	insertTestUser(t, database, userID)
	insertTestProject(t, database, projectID, userID)
	writeCompileTestFile(t, filepath.Join(projectsDir, projectID, "chapters", "book.typ"), "#set page(width: 120mm)")
	writeCompileTestFile(t, filepath.Join(projectsDir, projectID, "main.tex"), "\\documentclass{article}")

	if err := server.setProjectCompileEntry(projectID, "chapters/book.typ"); err != nil {
		t.Fatalf("setProjectCompileEntry returned error: %v", err)
	}

	entry, err := server.preferredCompileEntry(projectID, filepath.Join(projectsDir, projectID))
	if err != nil {
		t.Fatalf("preferredCompileEntry returned error: %v", err)
	}
	if entry != "chapters/book.typ" {
		t.Fatalf("preferredCompileEntry=%q want %q", entry, "chapters/book.typ")
	}
}

func TestPreferredCompileEntryFallsBackWhenStoredValueIsMissing(t *testing.T) {
	t.Parallel()

	server, database, projectsDir := newCompileTargetTestServer(t)
	defer database.Close()

	const userID = "user-1"
	const projectID = "project-1"

	insertTestUser(t, database, userID)
	insertTestProject(t, database, projectID, userID)
	writeCompileTestFile(t, filepath.Join(projectsDir, projectID, "main.tex"), "\\documentclass{article}")

	if err := server.setProjectCompileEntry(projectID, "missing.typ"); err != nil {
		t.Fatalf("setProjectCompileEntry returned error: %v", err)
	}

	entry, err := server.preferredCompileEntry(projectID, filepath.Join(projectsDir, projectID))
	if err != nil {
		t.Fatalf("preferredCompileEntry returned error: %v", err)
	}
	if entry != "main.tex" {
		t.Fatalf("preferredCompileEntry=%q want %q", entry, "main.tex")
	}
}

func TestHandleUpdateCompileTargetPersistsSelection(t *testing.T) {
	t.Parallel()

	server, database, projectsDir := newCompileTargetTestServer(t)
	defer database.Close()

	const userID = "user-1"
	const projectID = "project-1"

	insertTestUser(t, database, userID)
	insertTestProject(t, database, projectID, userID)
	writeCompileTestFile(t, filepath.Join(projectsDir, projectID, "docs", "paper.typ"), "= Hello")

	form := url.Values{}
	form.Set("entry", "docs/paper.typ")

	req := httptest.NewRequest(http.MethodPost, "/api/projects/"+projectID+"/compile-target", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req = req.WithContext(withRouteUserContext(req.Context(), projectID, &User{ID: userID, Email: "owner@example.com"}))

	rec := httptest.NewRecorder()
	server.handleUpdateCompileTarget(rec, req)

	if rec.Code != http.StatusNoContent {
		t.Fatalf("handleUpdateCompileTarget status=%d body=%q", rec.Code, rec.Body.String())
	}

	entry, err := server.getProjectCompileEntry(projectID)
	if err != nil {
		t.Fatalf("getProjectCompileEntry returned error: %v", err)
	}
	if entry != "docs/paper.typ" {
		t.Fatalf("stored compile entry=%q want %q", entry, "docs/paper.typ")
	}
}

func newCompileTargetTestServer(t *testing.T) (*Server, *sql.DB, string) {
	t.Helper()

	baseDir := t.TempDir()
	projectsDir := filepath.Join(baseDir, "projects")
	if err := os.MkdirAll(projectsDir, 0755); err != nil {
		t.Fatalf("failed to create projects dir: %v", err)
	}

	database, err := dbpkg.Open(filepath.Join(baseDir, "test.db"))
	if err != nil {
		t.Fatalf("failed to open test database: %v", err)
	}

	server, err := NewServer(database, "test-secret", projectsDir, "tectonic", "typst", "git")
	if err != nil {
		database.Close()
		t.Fatalf("failed to create test server: %v", err)
	}

	return server, database, projectsDir
}

func insertTestUser(t *testing.T, database *sql.DB, userID string) {
	t.Helper()

	hash, err := hashPassword("password123")
	if err != nil {
		t.Fatalf("failed to hash password: %v", err)
	}

	if _, err := database.Exec(
		"INSERT INTO users (id, email, password_hash, name, is_admin) VALUES (?, ?, ?, ?, 0)",
		userID, "owner@example.com", hash, "Owner",
	); err != nil {
		t.Fatalf("failed to insert user: %v", err)
	}
}

func insertTestProject(t *testing.T, database *sql.DB, projectID string, userID string) {
	t.Helper()

	if _, err := database.Exec(
		"INSERT INTO projects (id, user_id, name) VALUES (?, ?, ?)",
		projectID, userID, "Test Project",
	); err != nil {
		t.Fatalf("failed to insert project: %v", err)
	}
}

func writeCompileTestFile(t *testing.T, path string, content string) {
	t.Helper()

	if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		t.Fatalf("failed to create parent dir: %v", err)
	}
	if err := os.WriteFile(path, []byte(content), 0644); err != nil {
		t.Fatalf("failed to write file: %v", err)
	}
}

func withRouteUserContext(ctx context.Context, projectID string, user *User) context.Context {
	routeCtx := chi.NewRouteContext()
	routeCtx.URLParams.Add("id", projectID)
	ctx = context.WithValue(ctx, chi.RouteCtxKey, routeCtx)
	return context.WithValue(ctx, userContextKey, user)
}
