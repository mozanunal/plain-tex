package gitclient

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"strconv"
	"strings"
)

const managedExcludeMarker = "# poly-txt managed excludes"

var managedArtifactExcludePatterns = []string{
	"*.pdf",
	"*.aux",
	"*.log",
	"*.out",
	"*.toc",
	"*.nav",
	"*.snm",
	"*.fls",
	"*.fdb_latexmk",
	"*.synctex.gz",
	"*.xdv",
	"*.bbl",
	"*.blg",
	"*.bcf",
	"*.run.xml",
	"*.lof",
	"*.lot",
	"*.lol",
	"*.idx",
	"*.ind",
	"*.ilg",
}

type Client struct {
	GitBin string
}

type Auth struct {
	SSHPrivateKey string
}

type CloneOptions struct {
	Dir       string
	RemoteURL string
	Branch    string
	Auth      Auth
}

type SyncOptions struct {
	RepoDir     string
	RemoteURL   string
	Branch      string
	Auth        Auth
	AuthorName  string
	AuthorEmail string
	// Rebase replays local commits on top of the remote branch instead of
	// requiring a fast-forward. Used to recover from a diverged branch.
	Rebase bool
}

type CommitOptions struct {
	SyncOptions
	CommitMessage string
}

type RepoStatus struct {
	RepoPresent           bool
	CurrentBranch         string
	HasUncommittedChanges bool
	ChangedFiles          int
	ChangedFileNames      []string
	LastCommit            string
	Ahead                 int
	Behind                int
	TrackingRemote        bool
}

type PushResult struct {
	CommitCreated bool
	CommitHash    string
}

type CommandError struct {
	Args   []string
	Output string
}

func (e *CommandError) Error() string {
	if len(e.Args) == 0 {
		return "git command failed"
	}
	if e.Output == "" {
		return fmt.Sprintf("git %s failed", strings.Join(e.Args, " "))
	}
	return fmt.Sprintf("git %s failed: %s", strings.Join(e.Args, " "), e.Output)
}

func New(gitBin string) *Client {
	if strings.TrimSpace(gitBin) == "" {
		gitBin = "git"
	}
	return &Client{GitBin: gitBin}
}

func (c *Client) Clone(ctx context.Context, opts CloneOptions) error {
	if strings.TrimSpace(opts.Dir) == "" {
		return errors.New("clone directory is required")
	}
	if strings.TrimSpace(opts.RemoteURL) == "" {
		return errors.New("remote URL is required")
	}

	args := []string{"clone", "--origin", "origin"}
	if branch := strings.TrimSpace(opts.Branch); branch != "" {
		args = append(args, "--branch", branch, "--single-branch")
	}
	args = append(args, strings.TrimSpace(opts.RemoteURL), opts.Dir)
	if _, err := c.run(ctx, "", opts.Auth, nil, args...); err != nil {
		return err
	}

	return c.ensureLocalExcludes(opts.Dir)
}

func (c *Client) InitRepo(ctx context.Context, repoDir string, branch string) error {
	if strings.TrimSpace(repoDir) == "" {
		return errors.New("repo directory is required")
	}
	if strings.TrimSpace(branch) == "" {
		return errors.New("branch is required")
	}

	if _, err := c.run(ctx, repoDir, Auth{}, nil, "init", "-b", branch); err != nil {
		return err
	}

	return c.ensureLocalExcludes(repoDir)
}

func (c *Client) EnsureRemote(ctx context.Context, repoDir string, remoteURL string) error {
	if strings.TrimSpace(remoteURL) == "" {
		return errors.New("remote URL is required")
	}

	currentURL, err := c.output(ctx, repoDir, Auth{}, "remote", "get-url", "origin")
	if err != nil {
		if _, addErr := c.run(ctx, repoDir, Auth{}, nil, "remote", "add", "origin", remoteURL); addErr != nil {
			return addErr
		}
		return nil
	}

	if strings.TrimSpace(currentURL) == strings.TrimSpace(remoteURL) {
		return nil
	}

	_, err = c.run(ctx, repoDir, Auth{}, nil, "remote", "set-url", "origin", remoteURL)
	return err
}

func (c *Client) CurrentBranch(ctx context.Context, repoDir string) (string, error) {
	return c.output(ctx, repoDir, Auth{}, "branch", "--show-current")
}

func (c *Client) IsRepo(ctx context.Context, repoDir string) bool {
	output, err := c.output(ctx, repoDir, Auth{}, "rev-parse", "--is-inside-work-tree")
	return err == nil && strings.TrimSpace(output) == "true"
}

func (c *Client) Status(ctx context.Context, repoDir string) (RepoStatus, error) {
	status := RepoStatus{}
	if !c.IsRepo(ctx, repoDir) {
		return status, nil
	}
	status.RepoPresent = true

	currentBranch, err := c.CurrentBranch(ctx, repoDir)
	if err != nil {
		return RepoStatus{}, err
	}
	status.CurrentBranch = currentBranch

	lines, err := c.statusLines(ctx, repoDir)
	if err != nil {
		return RepoStatus{}, err
	}
	for _, line := range lines {
		status.ChangedFiles++
		status.ChangedFileNames = append(status.ChangedFileNames, line)
	}
	status.HasUncommittedChanges = status.ChangedFiles > 0

	if c.hasCommit(ctx, repoDir) {
		lastCommit, err := c.output(ctx, repoDir, Auth{}, "log", "-1", "--pretty=%h %s")
		if err != nil {
			return RepoStatus{}, err
		}
		status.LastCommit = lastCommit
	}

	status.Ahead, status.Behind, status.TrackingRemote = c.divergence(ctx, repoDir, currentBranch)

	return status, nil
}

func (c *Client) SwitchBranch(ctx context.Context, repoDir string, branch string) error {
	branch = strings.TrimSpace(branch)
	if branch == "" {
		return errors.New("branch is required")
	}

	currentBranch, err := c.CurrentBranch(ctx, repoDir)
	if err == nil && currentBranch == branch {
		return nil
	}

	if c.localBranchExists(ctx, repoDir, branch) {
		_, err = c.run(ctx, repoDir, Auth{}, nil, "checkout", branch)
		return err
	}

	if c.hasCommit(ctx, repoDir) {
		_, err = c.run(ctx, repoDir, Auth{}, nil, "checkout", "-b", branch)
		return err
	}

	_, err = c.run(ctx, repoDir, Auth{}, nil, "checkout", "-B", branch)
	return err
}

func (c *Client) Reset(ctx context.Context, repoDir string) error {
	if !c.IsRepo(ctx, repoDir) {
		return errors.New("project repository is not initialized")
	}
	if !c.hasCommit(ctx, repoDir) {
		return errors.New("no commits to reset to")
	}
	if _, err := c.run(ctx, repoDir, Auth{}, nil, "checkout", "--", "."); err != nil {
		return err
	}
	if _, err := c.run(ctx, repoDir, Auth{}, nil, "clean", "-fd"); err != nil {
		return err
	}
	return nil
}

func (c *Client) Pull(ctx context.Context, opts SyncOptions) error {
	if !c.IsRepo(ctx, opts.RepoDir) {
		return errors.New("project repository is not initialized")
	}
	if strings.TrimSpace(opts.Branch) == "" {
		return errors.New("branch is required")
	}
	if strings.TrimSpace(opts.RemoteURL) == "" {
		return errors.New("remote URL is required")
	}

	if dirty, err := c.worktreeDirty(ctx, opts.RepoDir); err != nil {
		return err
	} else if dirty {
		return errors.New("commit or discard local changes before pulling")
	}

	if err := c.EnsureRemote(ctx, opts.RepoDir, opts.RemoteURL); err != nil {
		return err
	}
	if _, err := c.run(ctx, opts.RepoDir, opts.Auth, nil, "fetch", "origin", opts.Branch); err != nil {
		return err
	}
	if !c.remoteBranchExists(ctx, opts.RepoDir, opts.Branch) {
		return fmt.Errorf("remote branch %q was not found", opts.Branch)
	}

	switch {
	case !c.hasCommit(ctx, opts.RepoDir):
		if _, err := c.run(ctx, opts.RepoDir, Auth{}, nil, "checkout", "-B", opts.Branch, "--track", "origin/"+opts.Branch); err != nil {
			return err
		}
	case !c.localBranchExists(ctx, opts.RepoDir, opts.Branch):
		if _, err := c.run(ctx, opts.RepoDir, Auth{}, nil, "checkout", "-b", opts.Branch, "--track", "origin/"+opts.Branch); err != nil {
			return err
		}
	default:
		if err := c.SwitchBranch(ctx, opts.RepoDir, opts.Branch); err != nil {
			return err
		}
		if opts.Rebase {
			name, email := authorIdentity(opts)
			args := []string{
				"-c", "user.name=" + name,
				"-c", "user.email=" + email,
				"pull", "--rebase", "origin", opts.Branch,
			}
			if _, err := c.run(ctx, opts.RepoDir, opts.Auth, nil, args...); err != nil {
				// A stopped rebase leaves the repo mid-operation with conflict
				// markers, and there is no way to resolve that from the editor,
				// so roll all the way back and report the original failure.
				_, _ = c.run(ctx, opts.RepoDir, Auth{}, nil, "rebase", "--abort")
				return err
			}
		} else {
			if _, err := c.run(ctx, opts.RepoDir, opts.Auth, nil, "pull", "--ff-only", "origin", opts.Branch); err != nil {
				return err
			}
		}
	}

	return c.ensureLocalExcludes(opts.RepoDir)
}

// ResetToRemote discards local commits and working tree changes so the branch
// matches the remote exactly. This permanently destroys unpushed work.
func (c *Client) ResetToRemote(ctx context.Context, opts SyncOptions) error {
	if !c.IsRepo(ctx, opts.RepoDir) {
		return errors.New("project repository is not initialized")
	}
	if strings.TrimSpace(opts.Branch) == "" {
		return errors.New("branch is required")
	}
	if strings.TrimSpace(opts.RemoteURL) == "" {
		return errors.New("remote URL is required")
	}

	if err := c.EnsureRemote(ctx, opts.RepoDir, opts.RemoteURL); err != nil {
		return err
	}
	if _, err := c.run(ctx, opts.RepoDir, opts.Auth, nil, "fetch", "origin", opts.Branch); err != nil {
		return err
	}
	if !c.remoteBranchExists(ctx, opts.RepoDir, opts.Branch) {
		return fmt.Errorf("remote branch %q was not found", opts.Branch)
	}
	if err := c.SwitchBranch(ctx, opts.RepoDir, opts.Branch); err != nil {
		return err
	}
	if _, err := c.run(ctx, opts.RepoDir, Auth{}, nil, "reset", "--hard", "origin/"+opts.Branch); err != nil {
		return err
	}
	if _, err := c.run(ctx, opts.RepoDir, Auth{}, nil, "clean", "-fd"); err != nil {
		return err
	}

	return c.ensureLocalExcludes(opts.RepoDir)
}

// divergence reports how many commits the local branch is ahead of and behind
// its remote counterpart, based on the last fetch. It performs no network I/O.
func (c *Client) divergence(ctx context.Context, repoDir string, branch string) (ahead int, behind int, tracking bool) {
	branch = strings.TrimSpace(branch)
	if branch == "" || !c.hasCommit(ctx, repoDir) || !c.remoteBranchExists(ctx, repoDir, branch) {
		return 0, 0, false
	}

	out, err := c.output(ctx, repoDir, Auth{}, "rev-list", "--left-right", "--count", "origin/"+branch+"...HEAD")
	if err != nil {
		return 0, 0, false
	}

	fields := strings.Fields(out)
	if len(fields) != 2 {
		return 0, 0, false
	}
	behind, err = strconv.Atoi(fields[0])
	if err != nil {
		return 0, 0, false
	}
	ahead, err = strconv.Atoi(fields[1])
	if err != nil {
		return 0, 0, false
	}

	return ahead, behind, true
}

func authorIdentity(opts SyncOptions) (string, string) {
	name := strings.TrimSpace(opts.AuthorName)
	if name == "" {
		name = "poly-txt"
	}
	email := strings.TrimSpace(opts.AuthorEmail)
	if email == "" {
		email = "poly-txt@local"
	}
	return name, email
}

func (c *Client) Push(ctx context.Context, opts CommitOptions) (PushResult, error) {
	if strings.TrimSpace(opts.RepoDir) == "" {
		return PushResult{}, errors.New("repo directory is required")
	}
	if strings.TrimSpace(opts.Branch) == "" {
		return PushResult{}, errors.New("branch is required")
	}
	if strings.TrimSpace(opts.RemoteURL) == "" {
		return PushResult{}, errors.New("remote URL is required")
	}

	if !c.IsRepo(ctx, opts.RepoDir) {
		if err := c.InitRepo(ctx, opts.RepoDir, opts.Branch); err != nil {
			return PushResult{}, err
		}
	}
	if err := c.ensureLocalExcludes(opts.RepoDir); err != nil {
		return PushResult{}, err
	}
	if err := c.EnsureRemote(ctx, opts.RepoDir, opts.RemoteURL); err != nil {
		return PushResult{}, err
	}
	if err := c.SwitchBranch(ctx, opts.RepoDir, opts.Branch); err != nil {
		return PushResult{}, err
	}
	if err := c.stageProjectContents(ctx, opts.RepoDir); err != nil {
		return PushResult{}, err
	}

	result := PushResult{}
	if dirty, err := c.worktreeDirty(ctx, opts.RepoDir); err != nil {
		return PushResult{}, err
	} else if dirty {
		authorName, authorEmail := authorIdentity(opts.SyncOptions)
		commitMessage := strings.TrimSpace(opts.CommitMessage)
		if commitMessage == "" {
			commitMessage = "Update project"
		}

		args := []string{
			"-c", "user.name=" + authorName,
			"-c", "user.email=" + authorEmail,
			"commit", "-m", commitMessage,
		}
		if _, err := c.run(ctx, opts.RepoDir, Auth{}, nil, args...); err != nil {
			return PushResult{}, err
		}

		result.CommitCreated = true
		result.CommitHash, _ = c.output(ctx, opts.RepoDir, Auth{}, "rev-parse", "--short", "HEAD")
	}

	if !c.hasCommit(ctx, opts.RepoDir) {
		return PushResult{}, errors.New("there is nothing to push yet")
	}
	if _, err := c.run(ctx, opts.RepoDir, opts.Auth, nil, "push", "-u", "origin", opts.Branch); err != nil {
		return PushResult{}, err
	}

	return result, nil
}

func (c *Client) stageProjectContents(ctx context.Context, repoDir string) error {
	lines, err := c.statusLines(ctx, repoDir)
	if err != nil {
		return err
	}

	for _, line := range lines {
		for _, filePath := range statusPaths(line) {
			if _, err := c.run(ctx, repoDir, Auth{}, nil, "add", "-A", "--", filePath); err != nil {
				return err
			}
		}
	}

	return nil
}

func (c *Client) worktreeDirty(ctx context.Context, repoDir string) (bool, error) {
	lines, err := c.statusLines(ctx, repoDir)
	if err != nil {
		return false, err
	}
	return len(lines) > 0, nil
}

func (c *Client) hasCommit(ctx context.Context, repoDir string) bool {
	cmd := exec.CommandContext(ctx, c.GitBin, "rev-parse", "--verify", "HEAD")
	cmd.Dir = repoDir
	cmd.Env = append(os.Environ(), "GIT_TERMINAL_PROMPT=0")
	return cmd.Run() == nil
}

func (c *Client) localBranchExists(ctx context.Context, repoDir string, branch string) bool {
	cmd := exec.CommandContext(ctx, c.GitBin, "show-ref", "--verify", "--quiet", "refs/heads/"+branch)
	cmd.Dir = repoDir
	cmd.Env = append(os.Environ(), "GIT_TERMINAL_PROMPT=0")
	return cmd.Run() == nil
}

func (c *Client) remoteBranchExists(ctx context.Context, repoDir string, branch string) bool {
	cmd := exec.CommandContext(ctx, c.GitBin, "show-ref", "--verify", "--quiet", "refs/remotes/origin/"+branch)
	cmd.Dir = repoDir
	cmd.Env = append(os.Environ(), "GIT_TERMINAL_PROMPT=0")
	return cmd.Run() == nil
}

func (c *Client) ensureLocalExcludes(repoDir string) error {
	excludePath := filepath.Join(repoDir, ".git", "info", "exclude")
	if _, err := os.Stat(excludePath); err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return err
	}

	existingContent, err := os.ReadFile(excludePath)
	if err != nil {
		return err
	}
	content := stripManagedExcludeBlock(string(existingContent))
	if content != "" {
		content += "\n\n"
	}
	content += managedExcludeBlock()

	return os.WriteFile(excludePath, []byte(content), 0644)
}

func (c *Client) statusLines(ctx context.Context, repoDir string) ([]string, error) {
	porcelain, err := c.output(ctx, repoDir, Auth{}, "status", "--porcelain")
	if err != nil {
		return nil, err
	}

	lines := make([]string, 0)
	for _, line := range strings.Split(strings.TrimSpace(porcelain), "\n") {
		line = strings.TrimRight(line, "\r")
		if strings.TrimSpace(line) == "" {
			continue
		}
		if statusLineIgnored(line) {
			continue
		}
		lines = append(lines, line)
	}

	return lines, nil
}

func statusLineIgnored(line string) bool {
	for _, filePath := range statusPaths(line) {
		if !isManagedArtifactPath(filePath) {
			return false
		}
	}
	return true
}

func statusPaths(line string) []string {
	if len(line) < 4 {
		return nil
	}

	pathSpec := strings.TrimSpace(line[3:])
	if pathSpec == "" {
		return nil
	}
	if strings.Contains(pathSpec, " -> ") {
		parts := strings.SplitN(pathSpec, " -> ", 2)
		return []string{strings.TrimSpace(parts[0]), strings.TrimSpace(parts[1])}
	}
	return []string{pathSpec}
}

func isManagedArtifactPath(filePath string) bool {
	filePath = path.Clean(filepath.ToSlash(strings.TrimSpace(filePath)))
	if filePath == "." || filePath == "" {
		return false
	}

	baseName := path.Base(filePath)
	for _, pattern := range managedArtifactExcludePatterns {
		if matched, _ := path.Match(pattern, baseName); matched {
			return true
		}
	}
	return false
}

func stripManagedExcludeBlock(content string) string {
	index := strings.Index(content, managedExcludeMarker)
	if index < 0 {
		return strings.TrimRight(content, "\n")
	}
	return strings.TrimRight(content[:index], "\n")
}

func managedExcludeBlock() string {
	return managedExcludeMarker + "\n" + strings.Join(managedArtifactExcludePatterns, "\n") + "\n"
}

func (c *Client) output(ctx context.Context, repoDir string, auth Auth, args ...string) (string, error) {
	output, err := c.run(ctx, repoDir, auth, nil, args...)
	return strings.TrimSpace(output), err
}

func (c *Client) run(ctx context.Context, repoDir string, auth Auth, extraEnv []string, args ...string) (string, error) {
	cmd := exec.CommandContext(ctx, c.GitBin, args...)
	if repoDir != "" {
		cmd.Dir = repoDir
	}

	env := append(os.Environ(), "GIT_TERMINAL_PROMPT=0")
	keyPath := ""
	if privateKey := strings.TrimSpace(auth.SSHPrivateKey); privateKey != "" {
		tempDir, err := os.MkdirTemp("", "poly-txt-git-key-*")
		if err != nil {
			return "", err
		}
		defer os.RemoveAll(tempDir)

		keyPath = filepath.Join(tempDir, "id_ed25519")
		normalizedKey := strings.ReplaceAll(privateKey, "\r\n", "\n")
		if !strings.HasSuffix(normalizedKey, "\n") {
			normalizedKey += "\n"
		}
		if err := os.WriteFile(keyPath, []byte(normalizedKey), 0600); err != nil {
			return "", err
		}
		env = append(env, "GIT_SSH_COMMAND=ssh -i "+keyPath+" -o IdentitiesOnly=yes -o StrictHostKeyChecking=accept-new -F /dev/null")
	}
	if len(extraEnv) > 0 {
		env = append(env, extraEnv...)
	}
	cmd.Env = env

	output, err := cmd.CombinedOutput()
	trimmedOutput := strings.TrimSpace(string(output))
	if err != nil {
		return "", &CommandError{
			Args:   append([]string(nil), args...),
			Output: trimmedOutput,
		}
	}

	return trimmedOutput, nil
}
