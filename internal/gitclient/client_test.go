package gitclient

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestPushCloneAndPullWithLocalRemote(t *testing.T) {
	t.Parallel()

	gitBin := requireGitBinary(t)
	client := New(gitBin)
	ctx := context.Background()

	remoteDir := filepath.Join(t.TempDir(), "remote.git")
	runGit(t, gitBin, "", "init", "--bare", remoteDir)

	workDir := filepath.Join(t.TempDir(), "worktree")
	if err := os.MkdirAll(workDir, 0755); err != nil {
		t.Fatalf("failed to create work tree: %v", err)
	}
	writeFile(t, filepath.Join(workDir, "main.tex"), "\\documentclass{article}\n\\begin{document}\ninitial\n\\end{document}\n")
	writeFile(t, filepath.Join(workDir, ".polytex-build", "main.pdf"), "compiled artifact")
	writeFile(t, filepath.Join(workDir, "figures", "plot.pdf"), "a committed figure")

	pushResult, err := client.Push(ctx, CommitOptions{
		SyncOptions: SyncOptions{
			RepoDir:     workDir,
			RemoteURL:   remoteDir,
			Branch:      "main",
			AuthorName:  "Test User",
			AuthorEmail: "test@example.com",
		},
		CommitMessage: "Initial commit",
	})
	if err != nil {
		t.Fatalf("Push returned error: %v", err)
	}
	if !pushResult.CommitCreated {
		t.Fatalf("expected push to create an initial commit")
	}

	cloneDir := filepath.Join(t.TempDir(), "clone")
	if err := client.Clone(ctx, CloneOptions{
		Dir:       cloneDir,
		RemoteURL: remoteDir,
		Branch:    "main",
	}); err != nil {
		t.Fatalf("Clone returned error: %v", err)
	}

	if _, err := os.Stat(filepath.Join(cloneDir, ".polytex-build", "main.pdf")); !os.IsNotExist(err) {
		t.Fatalf("expected compiled build output to stay out of the repository, stat err=%v", err)
	}
	if _, err := os.Stat(filepath.Join(cloneDir, "figures", "plot.pdf")); err != nil {
		t.Fatalf("expected a committed figure PDF to be tracked, stat err=%v", err)
	}

	writeFile(t, filepath.Join(cloneDir, "main.tex"), "\\documentclass{article}\n\\begin{document}\nremote update\n\\end{document}\n")
	runGit(t, gitBin, cloneDir, "-c", "user.name=Remote User", "-c", "user.email=remote@example.com", "commit", "-am", "Remote update")
	runGit(t, gitBin, cloneDir, "push", "origin", "main")

	if err := client.Pull(ctx, SyncOptions{
		RepoDir:   workDir,
		RemoteURL: remoteDir,
		Branch:    "main",
	}); err != nil {
		t.Fatalf("Pull returned error: %v", err)
	}

	content, err := os.ReadFile(filepath.Join(workDir, "main.tex"))
	if err != nil {
		t.Fatalf("failed to read pulled file: %v", err)
	}
	if !strings.Contains(string(content), "remote update") {
		t.Fatalf("expected pulled file to contain remote change, got %q", string(content))
	}

	status, err := client.Status(ctx, workDir)
	if err != nil {
		t.Fatalf("Status returned error: %v", err)
	}
	if status.HasUncommittedChanges {
		t.Fatalf("expected clean work tree after pull, got %#v", status)
	}
}

func TestPullRejectsDirtyWorktree(t *testing.T) {
	t.Parallel()

	gitBin := requireGitBinary(t)
	client := New(gitBin)
	ctx := context.Background()

	remoteDir := filepath.Join(t.TempDir(), "remote.git")
	runGit(t, gitBin, "", "init", "--bare", remoteDir)

	workDir := filepath.Join(t.TempDir(), "worktree")
	if err := os.MkdirAll(workDir, 0755); err != nil {
		t.Fatalf("failed to create work tree: %v", err)
	}
	writeFile(t, filepath.Join(workDir, "main.tex"), "initial\n")

	if _, err := client.Push(ctx, CommitOptions{
		SyncOptions: SyncOptions{
			RepoDir:     workDir,
			RemoteURL:   remoteDir,
			Branch:      "main",
			AuthorName:  "Test User",
			AuthorEmail: "test@example.com",
		},
		CommitMessage: "Initial commit",
	}); err != nil {
		t.Fatalf("Push returned error: %v", err)
	}

	writeFile(t, filepath.Join(workDir, "main.tex"), "local dirty change\n")

	err := client.Pull(ctx, SyncOptions{
		RepoDir:   workDir,
		RemoteURL: remoteDir,
		Branch:    "main",
	})
	if err == nil {
		t.Fatalf("expected dirty work tree pull to fail")
	}
	if !strings.Contains(err.Error(), "commit or discard local changes") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestIsManagedArtifactPath(t *testing.T) {
	t.Parallel()

	cases := []struct {
		path string
		want bool
	}{
		{path: ".polytex-build/main.pdf", want: true},
		{path: ".polytex-build/main.log", want: true},
		{path: ".polytex-build", want: true},
		{path: "main.pdf", want: false},
		{path: "chapters/output.pdf", want: false},
		{path: "figures/plot.pdf", want: false},
		{path: "images/figure.png", want: false},
		{path: "docs/paper.typ", want: false},
	}

	for _, tc := range cases {
		tc := tc
		t.Run(tc.path, func(t *testing.T) {
			t.Parallel()
			got := isManagedArtifactPath(tc.path)
			if got != tc.want {
				t.Fatalf("isManagedArtifactPath(%q)=%v want %v", tc.path, got, tc.want)
			}
		})
	}
}

func requireGitBinary(t *testing.T) string {
	t.Helper()

	gitBin, err := exec.LookPath("git")
	if err != nil {
		t.Skip("git binary is not available")
	}
	return gitBin
}

func runGit(t *testing.T, gitBin string, dir string, args ...string) {
	t.Helper()

	cmd := exec.Command(gitBin, args...)
	if dir != "" {
		cmd.Dir = dir
	}
	output, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %s failed: %v\n%s", strings.Join(args, " "), err, string(output))
	}
}

func writeFile(t *testing.T, path string, content string) {
	t.Helper()

	if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		t.Fatalf("failed to create parent directory for %s: %v", path, err)
	}
	if err := os.WriteFile(path, []byte(content), 0644); err != nil {
		t.Fatalf("failed to write %s: %v", path, err)
	}
}
