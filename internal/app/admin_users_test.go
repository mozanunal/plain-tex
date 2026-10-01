package app

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/go-chi/chi/v5"
)

func adminActionRequest(t *testing.T, actor *User, targetID string) (*httptest.ResponseRecorder, *http.Request) {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, "/admin/users/"+targetID+"/action", nil)
	routeCtx := chi.NewRouteContext()
	routeCtx.URLParams.Add("userID", targetID)
	ctx := context.WithValue(req.Context(), chi.RouteCtxKey, routeCtx)
	ctx = context.WithValue(ctx, userContextKey, actor)
	return httptest.NewRecorder(), req.WithContext(ctx)
}

func insertTestUserFull(t *testing.T, s *Server, id, email string, admin bool) {
	t.Helper()
	adminVal := 0
	if admin {
		adminVal = 1
	}
	hash, _ := hashPassword("password123")
	if _, err := s.db.Exec(
		"INSERT INTO users (id, email, password_hash, name, is_admin) VALUES (?, ?, ?, ?, ?)",
		id, email, hash, "", adminVal,
	); err != nil {
		t.Fatalf("insert user: %v", err)
	}
}

func userColumn[T any](t *testing.T, s *Server, id, column string) T {
	t.Helper()
	var value T
	if err := s.db.QueryRow("SELECT "+column+" FROM users WHERE id = ?", id).Scan(&value); err != nil {
		t.Fatalf("read %s: %v", column, err)
	}
	return value
}

func TestAdminCannotDemoteOrDeleteLastAdmin(t *testing.T) {
	t.Parallel()
	server, database, _ := newCompileTargetTestServer(t)
	defer database.Close()

	admin := &User{ID: "admin-1", Email: "admin@example.com", IsAdmin: true}
	insertTestUserFull(t, server, admin.ID, admin.Email, true)
	insertTestUserFull(t, server, "user-2", "user2@example.com", false)

	// Demoting the only admin is refused.
	rec, req := adminActionRequest(t, admin, admin.ID)
	server.handleAdminSetUserAdmin(false)(rec, req)
	if got := userColumn[int](t, server, admin.ID, "is_admin"); got != 1 {
		t.Fatalf("last admin was demoted (is_admin=%d)", got)
	}

	// Deleting yourself as admin is refused.
	rec, req = adminActionRequest(t, admin, admin.ID)
	server.handleAdminDeleteUser(rec, req)
	if got := userColumn[int](t, server, admin.ID, "is_admin"); got != 1 {
		t.Fatalf("admin deleted own account")
	}
}

func TestAdminDisableBumpsSessionAndSetsFlag(t *testing.T) {
	t.Parallel()
	server, database, _ := newCompileTargetTestServer(t)
	defer database.Close()

	admin := &User{ID: "admin-1", Email: "admin@example.com", IsAdmin: true}
	insertTestUserFull(t, server, admin.ID, admin.Email, true)
	insertTestUserFull(t, server, "user-2", "user2@example.com", false)

	before := userColumn[int](t, server, "user-2", "session_version")
	rec, req := adminActionRequest(t, admin, "user-2")
	server.handleAdminSetUserDisabled(true)(rec, req)

	if got := userColumn[int](t, server, "user-2", "disabled"); got != 1 {
		t.Fatalf("user not disabled (disabled=%d)", got)
	}
	if after := userColumn[int](t, server, "user-2", "session_version"); after <= before {
		t.Fatalf("session_version not bumped on disable: before=%d after=%d", before, after)
	}
}

func TestAdminDeleteRefusesUserWithProjects(t *testing.T) {
	t.Parallel()
	server, database, _ := newCompileTargetTestServer(t)
	defer database.Close()

	admin := &User{ID: "admin-1", Email: "admin@example.com", IsAdmin: true}
	insertTestUserFull(t, server, admin.ID, admin.Email, true)
	insertTestUserFull(t, server, "owner-2", "owner2@example.com", false)
	insertTestProject(t, database, "p-1", "owner-2")

	rec, req := adminActionRequest(t, admin, "owner-2")
	server.handleAdminDeleteUser(rec, req)

	if _, err := server.db.Exec("SELECT 1"); err != nil {
		t.Fatalf("db error: %v", err)
	}
	var stillThere int
	server.db.QueryRow("SELECT COUNT(*) FROM users WHERE id = ?", "owner-2").Scan(&stillThere)
	if stillThere != 1 {
		t.Fatalf("user owning projects was deleted")
	}
}

func TestNonAdminCannotRunLifecycleActions(t *testing.T) {
	t.Parallel()
	server, database, _ := newCompileTargetTestServer(t)
	defer database.Close()

	insertTestUserFull(t, server, "admin-1", "admin@example.com", true)
	attacker := &User{ID: "user-2", Email: "user2@example.com", IsAdmin: false}
	insertTestUserFull(t, server, attacker.ID, attacker.Email, false)

	rec, req := adminActionRequest(t, attacker, "admin-1")
	server.handleAdminDeleteUser(rec, req)
	if rec.Code != http.StatusForbidden {
		t.Fatalf("non-admin lifecycle action status=%d want 403", rec.Code)
	}
}
