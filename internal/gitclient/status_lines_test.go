package gitclient

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// The porcelain format keeps two status columns before the path, and the first
// column is blank for a plain unstaged edit. Trimming anywhere in that pipeline
// shifts the filename, which previously rendered "ideas.md" as "deas.md".
func TestStatusLinesKeepsLeadingColumnForFirstEntry(t *testing.T) {
	client := New("git")
	ctx := context.Background()
	repoDir := t.TempDir()

	mustRun := func(args ...string) {
		t.Helper()
		if _, err := client.run(ctx, repoDir, Auth{}, nil, args...); err != nil {
			t.Fatalf("git %s: %v", strings.Join(args, " "), err)
		}
	}

	mustRun("init", "-b", "main")
	mustRun("config", "user.email", "test@example.com")
	mustRun("config", "user.name", "Test")

	for _, name := range []string{"ideas.md", "notes.md"} {
		if err := os.WriteFile(filepath.Join(repoDir, name), []byte("original\n"), 0644); err != nil {
			t.Fatalf("write %s: %v", name, err)
		}
	}
	mustRun("add", ".")
	mustRun("commit", "-m", "base")

	// Modify both so there is a first entry and a later one.
	for _, name := range []string{"ideas.md", "notes.md"} {
		if err := os.WriteFile(filepath.Join(repoDir, name), []byte("changed\n"), 0644); err != nil {
			t.Fatalf("rewrite %s: %v", name, err)
		}
	}

	lines, err := client.statusLines(ctx, repoDir)
	if err != nil {
		t.Fatalf("statusLines: %v", err)
	}
	if len(lines) != 2 {
		t.Fatalf("got %d status lines, want 2: %q", len(lines), lines)
	}

	for _, line := range lines {
		if len(line) < 4 {
			t.Fatalf("status line %q is too short to carry both columns", line)
		}
		if line[0] != ' ' || line[1] != 'M' || line[2] != ' ' {
			t.Fatalf("status line %q does not start with the unstaged-modify columns %q", line, " M ")
		}

		// This is the slice the UI performs.
		name := line[3:]
		if name != "ideas.md" && name != "notes.md" {
			t.Fatalf("parsed filename %q lost characters (line %q)", name, line)
		}
	}
}
