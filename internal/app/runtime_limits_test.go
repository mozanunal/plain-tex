package app

import (
	"context"
	"net/http"
	"net/http/httptest"
	"net/url"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// TestCompileTimesOutOnRunawayProcess verifies a compile that never terminates
// is stopped and reported as a timeout rather than hanging the request.
func TestCompileTimesOutOnRunawayProcess(t *testing.T) {
	t.Parallel()
	server, database, projectsDir := newCompileTargetTestServer(t)
	defer database.Close()

	const userID, projectID = "user-1", "project-1"
	insertTestUser(t, database, userID)
	insertTestProject(t, database, projectID, userID)
	writeCompileTestFile(t, filepath.Join(projectsDir, projectID, "main.tex"), "loop")

	// A fake tectonic that never exits, to exercise the wall-clock timeout.
	fakeBin := filepath.Join(t.TempDir(), "tectonic-hang")
	writeExecutable(t, fakeBin, "#!/bin/sh\nwhile true; do sleep 1; done\n")
	server.compiler = NewCompiler(fakeBin, "typst")

	// Shrink the timeout for the test by compiling with a short-deadline context
	// through the compiler directly, mirroring what the handler does.
	ctx, cancel := context.WithTimeout(context.Background(), 1*time.Second)
	defer cancel()

	start := time.Now()
	_, _, err := server.compiler.Compile(ctx, filepath.Join(projectsDir, projectID), "main.tex")
	elapsed := time.Since(start)

	if err == nil {
		t.Fatal("expected the runaway compile to be killed")
	}
	if elapsed > 10*time.Second {
		t.Fatalf("compile was not killed promptly: took %s", elapsed)
	}
}

func TestLoginRateLimiterBlocksAfterBurst(t *testing.T) {
	t.Parallel()
	server, database, _ := newCompileTargetTestServer(t)
	defer database.Close()

	limiter := newIPRateLimiter(0, 3)
	handler := limiter.middleware(http.HandlerFunc(server.handleLogin))

	form := url.Values{"email": {"nobody@example.com"}, "password": {"wrong"}}
	do := func() int {
		req := httptest.NewRequest(http.MethodPost, "/login", strings.NewReader(form.Encode()))
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		req.RemoteAddr = "203.0.113.5:12345"
		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, req)
		return rec.Code
	}

	for i := 0; i < 3; i++ {
		if code := do(); code == http.StatusTooManyRequests {
			t.Fatalf("attempt %d throttled too early", i+1)
		}
	}
	if code := do(); code != http.StatusTooManyRequests {
		t.Fatalf("fourth attempt status=%d want 429", code)
	}
}

func TestKeyedMutexSerializesSameKey(t *testing.T) {
	t.Parallel()
	km := newKeyedMutex()

	unlock := km.Lock("p1")
	acquired := make(chan struct{})
	go func() {
		release := km.Lock("p1")
		close(acquired)
		release()
	}()

	select {
	case <-acquired:
		t.Fatal("second Lock on the same key acquired while held")
	case <-time.After(50 * time.Millisecond):
	}
	unlock()
	select {
	case <-acquired:
	case <-time.After(time.Second):
		t.Fatal("second Lock never acquired after release")
	}

	// A different key is independent and must not block.
	other := km.Lock("p2")
	other()
}
