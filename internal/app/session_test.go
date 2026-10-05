package app

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestAuthMiddlewareRejectsStaleAndDisabledSessions(t *testing.T) {
	t.Parallel()
	server, database, _ := newCompileTargetTestServer(t)
	defer database.Close()

	insertTestUserFull(t, server, "user-1", "user1@example.com", false)

	protected := server.authMiddleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))

	call := func(token string) int {
		req := httptest.NewRequest(http.MethodGet, "/", nil)
		req.AddCookie(&http.Cookie{Name: "token", Value: token})
		rec := httptest.NewRecorder()
		protected.ServeHTTP(rec, req)
		return rec.Code
	}

	// A token for the current session version is accepted.
	validToken, err := server.createToken("user-1", "user1@example.com", 0)
	if err != nil {
		t.Fatalf("createToken: %v", err)
	}
	if code := call(validToken); code != http.StatusOK {
		t.Fatalf("valid session rejected: status=%d", code)
	}

	// After a bump, the old token no longer matches and is redirected to login.
	if err := server.bumpSessionVersion("user-1"); err != nil {
		t.Fatalf("bump: %v", err)
	}
	if code := call(validToken); code != http.StatusSeeOther {
		t.Fatalf("stale session accepted: status=%d want 303", code)
	}

	// A token minted for the new version works again, until the account is disabled.
	freshToken, _ := server.createToken("user-1", "user1@example.com", 1)
	if code := call(freshToken); code != http.StatusOK {
		t.Fatalf("fresh session rejected: status=%d", code)
	}
	if _, err := server.db.Exec("UPDATE users SET disabled = 1 WHERE id = ?", "user-1"); err != nil {
		t.Fatalf("disable: %v", err)
	}
	if code := call(freshToken); code != http.StatusSeeOther {
		t.Fatalf("disabled account accepted: status=%d want 303", code)
	}
}
