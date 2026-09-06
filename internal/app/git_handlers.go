package app

import (
	"database/sql"
	"encoding/json"
	"errors"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"unicode"

	"github.com/google/uuid"
	"github.com/mozanunal/poly-txt/internal/gitclient"
)

type projectGitConfigRecord struct {
	ProjectID string
	RemoteURL string
	Branch    string
	LastSync  string
	Created   string
	Updated   string
}

type userGitKeyRecord struct {
	UserID              string
	PublicKey           string
	PrivateKeyEncrypted string
	Fingerprint         string
	Created             string
	Updated             string
}

type GitStatusResponse struct {
	Configured            bool     `json:"configured"`
	ProjectID             string   `json:"projectId"`
	RemoteURL             string   `json:"remoteURL"`
	Branch                string   `json:"branch"`
	HasSSHKey             bool     `json:"hasSSHKey"`
	SSHKeyFingerprint     string   `json:"sshKeyFingerprint"`
	LastSync              string   `json:"lastSync"`
	RepoPresent           bool     `json:"repoPresent"`
	CurrentBranch         string   `json:"currentBranch"`
	HasUncommittedChanges bool     `json:"hasUncommittedChanges"`
	ChangedFiles          int      `json:"changedFiles"`
	ChangedFileNames      []string `json:"changedFileNames"`
	LastCommit            string   `json:"lastCommit"`
	Ahead                 int      `json:"ahead"`
	Behind                int      `json:"behind"`
	Diverged              bool     `json:"diverged"`
	TrackingRemote        bool     `json:"trackingRemote"`
}

func (s *Server) handleGenerateUserGitKey(w http.ResponseWriter, r *http.Request) {
	user := getUserFromContext(r.Context())

	publicKey, privateKey, fingerprint, err := gitclient.GenerateUserSSHKeyPair(user.Email)
	if err != nil {
		redirectWithMessage(w, r, "/settings", "", "Failed to generate SSH key")
		return
	}

	encryptedPrivateKey, err := gitclient.EncryptSecret(s.jwtSecret, privateKey)
	if err != nil {
		redirectWithMessage(w, r, "/settings", "", "Failed to store SSH key")
		return
	}

	if err := s.upsertUserGitKey(userGitKeyRecord{
		UserID:              user.ID,
		PublicKey:           publicKey,
		PrivateKeyEncrypted: encryptedPrivateKey,
		Fingerprint:         fingerprint,
	}); err != nil {
		redirectWithMessage(w, r, "/settings", "", "Failed to save SSH key")
		return
	}

	redirectWithMessage(w, r, "/settings", "SSH key generated", "")
}

func (s *Server) handleDeleteUserGitKey(w http.ResponseWriter, r *http.Request) {
	user := getUserFromContext(r.Context())

	if _, err := s.db.Exec("DELETE FROM user_git_keys WHERE user_id = ?", user.ID); err != nil {
		redirectWithMessage(w, r, "/settings", "", "Failed to delete SSH key")
		return
	}

	redirectWithMessage(w, r, "/settings", "SSH key deleted", "")
}

func (s *Server) handleCloneProject(w http.ResponseWriter, r *http.Request) {
	user := getUserFromContext(r.Context())

	remoteURL := strings.TrimSpace(r.FormValue("remoteURL"))
	if remoteURL == "" {
		redirectWithMessage(w, r, "/", "", "Remote URL is required")
		return
	}

	branch, err := normalizeGitBranch(r.FormValue("branch"), true)
	if err != nil {
		redirectWithMessage(w, r, "/", "", err.Error())
		return
	}

	_, auth, err := s.getOptionalUserGitAuth(user.ID)
	if err != nil {
		redirectWithMessage(w, r, "/", "", gitKeyLoadErrorMessage(err))
		return
	}
	if err := ensureUserGitKeyForRemote(remoteURL, auth); err != nil {
		redirectWithMessage(w, r, "/", "", err.Error())
		return
	}

	projectID := uuid.New().String()
	projectDir := filepath.Join(s.projectsDir, projectID)
	if err := os.MkdirAll(projectDir, 0755); err != nil {
		redirectWithMessage(w, r, "/", "", "Failed to create project directory")
		return
	}

	if err := s.git.Clone(r.Context(), gitclient.CloneOptions{
		Dir:       projectDir,
		RemoteURL: remoteURL,
		Branch:    branch,
		Auth:      auth,
	}); err != nil {
		os.RemoveAll(projectDir)
		redirectWithMessage(w, r, "/", "", renderGitError(err))
		return
	}

	if branch == "" {
		branch, err = s.git.CurrentBranch(r.Context(), projectDir)
		if err != nil {
			os.RemoveAll(projectDir)
			redirectWithMessage(w, r, "/", "", "Repository cloned but branch detection failed")
			return
		}
	}

	projectName := strings.TrimSpace(r.FormValue("name"))
	if projectName == "" {
		projectName = defaultProjectNameFromRemote(remoteURL)
	}

	tx, err := s.db.Begin()
	if err != nil {
		os.RemoveAll(projectDir)
		redirectWithMessage(w, r, "/", "", "Failed to create project")
		return
	}
	defer tx.Rollback()

	if _, err := tx.Exec(
		"INSERT INTO projects (id, user_id, name) VALUES (?, ?, ?)",
		projectID, user.ID, projectName,
	); err != nil {
		os.RemoveAll(projectDir)
		redirectWithMessage(w, r, "/", "", "Failed to create project")
		return
	}

	if _, err := tx.Exec(
		`INSERT INTO project_git_configs
		(project_id, remote_url, branch, last_sync)
		VALUES (?, ?, ?, datetime('now'))`,
		projectID, remoteURL, branch,
	); err != nil {
		os.RemoveAll(projectDir)
		redirectWithMessage(w, r, "/", "", "Failed to save Git configuration")
		return
	}

	if err := tx.Commit(); err != nil {
		os.RemoveAll(projectDir)
		redirectWithMessage(w, r, "/", "", "Failed to save cloned project")
		return
	}

	redirectWithMessage(w, r, "/editor/"+projectID, "Repository cloned", "")
}

func (s *Server) handleGitStatus(w http.ResponseWriter, r *http.Request) {
	user := getUserFromContext(r.Context())
	access, ok := s.requireProjectAccess(w, r, projectAccessRead)
	if !ok {
		return
	}

	userKey, err := s.getUserGitKeySummary(user.ID)
	if err != nil {
		http.Error(w, "Failed to load your Git key", http.StatusInternalServerError)
		return
	}

	cfg, err := s.getProjectGitConfig(access.ProjectID)
	if err == sql.ErrNoRows {
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(GitStatusResponse{
			Configured:        false,
			ProjectID:         access.ProjectID,
			HasSSHKey:         userKey != nil,
			SSHKeyFingerprint: userGitFingerprint(userKey),
		})
		return
	}
	if err != nil {
		http.Error(w, "Failed to load Git status", http.StatusInternalServerError)
		return
	}

	repoStatus, err := s.git.Status(r.Context(), filepath.Join(s.projectsDir, access.ProjectID))
	if err != nil {
		http.Error(w, renderGitError(err), http.StatusBadRequest)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(GitStatusResponse{
		Configured:            true,
		ProjectID:             cfg.ProjectID,
		RemoteURL:             cfg.RemoteURL,
		Branch:                cfg.Branch,
		HasSSHKey:             userKey != nil,
		SSHKeyFingerprint:     userGitFingerprint(userKey),
		LastSync:              cfg.LastSync,
		RepoPresent:           repoStatus.RepoPresent,
		CurrentBranch:         repoStatus.CurrentBranch,
		HasUncommittedChanges: repoStatus.HasUncommittedChanges,
		ChangedFiles:          repoStatus.ChangedFiles,
		ChangedFileNames:      repoStatus.ChangedFileNames,
		LastCommit:            repoStatus.LastCommit,
		Ahead:                 repoStatus.Ahead,
		Behind:                repoStatus.Behind,
		Diverged:              repoStatus.Ahead > 0 && repoStatus.Behind > 0,
		TrackingRemote:        repoStatus.TrackingRemote,
	})
}

func (s *Server) handleGitConfig(w http.ResponseWriter, r *http.Request) {
	access, ok := s.requireProjectAccess(w, r, projectAccessManage)
	if !ok {
		return
	}

	remoteURL := strings.TrimSpace(r.FormValue("remoteURL"))
	if remoteURL == "" {
		http.Error(w, "Remote URL is required", http.StatusBadRequest)
		return
	}

	projectDir := filepath.Join(s.projectsDir, access.ProjectID)
	if err := os.MkdirAll(projectDir, 0755); err != nil {
		http.Error(w, "Failed to prepare project repository", http.StatusInternalServerError)
		return
	}

	branch, err := normalizeGitBranch(r.FormValue("branch"), true)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}

	if s.git.IsRepo(r.Context(), projectDir) {
		currentBranch, branchErr := s.git.CurrentBranch(r.Context(), projectDir)
		if branchErr != nil {
			http.Error(w, renderGitError(branchErr), http.StatusBadRequest)
			return
		}
		if branch == "" {
			branch = currentBranch
		} else if branch != currentBranch {
			if err := s.git.SwitchBranch(r.Context(), projectDir, branch); err != nil {
				http.Error(w, renderGitError(err), http.StatusBadRequest)
				return
			}
		}
	} else {
		if branch == "" {
			branch = "main"
		}
		if err := s.git.InitRepo(r.Context(), projectDir, branch); err != nil {
			http.Error(w, renderGitError(err), http.StatusBadRequest)
			return
		}
	}

	if err := s.git.EnsureRemote(r.Context(), projectDir, remoteURL); err != nil {
		http.Error(w, renderGitError(err), http.StatusBadRequest)
		return
	}

	if err := s.upsertProjectGitConfig(projectGitConfigRecord{
		ProjectID: access.ProjectID,
		RemoteURL: remoteURL,
		Branch:    branch,
	}); err != nil {
		http.Error(w, "Failed to save Git configuration", http.StatusInternalServerError)
		return
	}

	s.touchProject(access.ProjectID)

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]string{
		"status": "Git sync settings saved",
		"branch": branch,
	})
}

func (s *Server) handleGitPull(w http.ResponseWriter, r *http.Request) {
	s.pullWithStrategy(w, r, false)
}

// handleGitPullRebase recovers a diverged branch by replaying the local commits
// on top of the remote branch. It is a separate action from Pull because it
// rewrites local history, so it should never happen implicitly.
func (s *Server) handleGitPullRebase(w http.ResponseWriter, r *http.Request) {
	s.pullWithStrategy(w, r, true)
}

func (s *Server) pullWithStrategy(w http.ResponseWriter, r *http.Request, rebase bool) {
	user := getUserFromContext(r.Context())
	access, ok := s.requireProjectAccess(w, r, projectAccessWrite)
	if !ok {
		return
	}

	cfg, err := s.getProjectGitConfig(access.ProjectID)
	if err == sql.ErrNoRows {
		http.Error(w, "Git sync is not configured for this project", http.StatusNotFound)
		return
	}
	if err != nil {
		http.Error(w, "Failed to load Git configuration", http.StatusInternalServerError)
		return
	}

	_, auth, err := s.getOptionalUserGitAuth(user.ID)
	if err != nil {
		http.Error(w, gitKeyLoadErrorMessage(err), http.StatusBadRequest)
		return
	}
	if err := ensureUserGitKeyForRemote(cfg.RemoteURL, auth); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}

	if err := s.git.Pull(r.Context(), gitclient.SyncOptions{
		RepoDir:     filepath.Join(s.projectsDir, access.ProjectID),
		RemoteURL:   cfg.RemoteURL,
		Branch:      cfg.Branch,
		Auth:        auth,
		AuthorName:  user.Name,
		AuthorEmail: user.Email,
		Rebase:      rebase,
	}); err != nil {
		http.Error(w, renderPullError(err, rebase), http.StatusBadRequest)
		return
	}

	s.updateProjectGitLastSync(access.ProjectID)
	s.touchProject(access.ProjectID)

	status := "Pulled latest changes"
	if rebase {
		status = "Rebased your commits onto the remote branch"
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]string{"status": status})
}

// handleGitResetToRemote discards local commits so the branch matches the
// remote. It permanently destroys unpushed work, so the UI confirms first.
func (s *Server) handleGitResetToRemote(w http.ResponseWriter, r *http.Request) {
	user := getUserFromContext(r.Context())
	access, ok := s.requireProjectAccess(w, r, projectAccessWrite)
	if !ok {
		return
	}

	cfg, err := s.getProjectGitConfig(access.ProjectID)
	if err == sql.ErrNoRows {
		http.Error(w, "Git sync is not configured for this project", http.StatusNotFound)
		return
	}
	if err != nil {
		http.Error(w, "Failed to load Git configuration", http.StatusInternalServerError)
		return
	}

	_, auth, err := s.getOptionalUserGitAuth(user.ID)
	if err != nil {
		http.Error(w, gitKeyLoadErrorMessage(err), http.StatusBadRequest)
		return
	}
	if err := ensureUserGitKeyForRemote(cfg.RemoteURL, auth); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}

	if err := s.git.ResetToRemote(r.Context(), gitclient.SyncOptions{
		RepoDir:   filepath.Join(s.projectsDir, access.ProjectID),
		RemoteURL: cfg.RemoteURL,
		Branch:    cfg.Branch,
		Auth:      auth,
	}); err != nil {
		http.Error(w, renderGitError(err), http.StatusBadRequest)
		return
	}

	s.updateProjectGitLastSync(access.ProjectID)
	s.touchProject(access.ProjectID)

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]string{
		"status": "Local commits discarded. This project now matches the remote.",
	})
}

// renderPullError explains the two failures a user cannot act on from the raw
// git output alone: a refused fast-forward, and a rebase stopped by conflicts.
func renderPullError(err error, rebase bool) string {
	message := renderGitError(err)

	switch {
	case !rebase && strings.Contains(message, "Not possible to fast-forward"):
		return "Your branch and the remote have both moved on, so a plain pull would have to merge.\n\n" +
			"Use \"Pull with rebase\" to replay your local commits on top of the remote, " +
			"or discard your local commits to match the remote.\n\n" + message
	case rebase && strings.Contains(strings.ToLower(message), "conflict"):
		return "The rebase hit conflicting edits, so it was rolled back and nothing changed.\n\n" +
			"The same lines were changed locally and on the remote. Resolve it by discarding " +
			"your local commits to match the remote, or by editing the conflicting file " +
			"so it no longer clashes.\n\n" + message
	}

	return message
}

func (s *Server) handleGitPush(w http.ResponseWriter, r *http.Request) {
	user := getUserFromContext(r.Context())
	access, ok := s.requireProjectAccess(w, r, projectAccessWrite)
	if !ok {
		return
	}

	cfg, err := s.getProjectGitConfig(access.ProjectID)
	if err == sql.ErrNoRows {
		http.Error(w, "Git sync is not configured for this project", http.StatusNotFound)
		return
	}
	if err != nil {
		http.Error(w, "Failed to load Git configuration", http.StatusInternalServerError)
		return
	}

	_, auth, err := s.getOptionalUserGitAuth(user.ID)
	if err != nil {
		http.Error(w, gitKeyLoadErrorMessage(err), http.StatusBadRequest)
		return
	}
	if err := ensureUserGitKeyForRemote(cfg.RemoteURL, auth); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}

	pushResult, err := s.git.Push(r.Context(), gitclient.CommitOptions{
		SyncOptions: gitclient.SyncOptions{
			RepoDir:     filepath.Join(s.projectsDir, access.ProjectID),
			RemoteURL:   cfg.RemoteURL,
			Branch:      cfg.Branch,
			Auth:        auth,
			AuthorName:  strings.TrimSpace(user.Name),
			AuthorEmail: strings.TrimSpace(user.Email),
		},
		CommitMessage: strings.TrimSpace(r.FormValue("commitMessage")),
	})
	if err != nil {
		http.Error(w, renderGitError(err), http.StatusBadRequest)
		return
	}

	s.updateProjectGitLastSync(access.ProjectID)
	s.touchProject(access.ProjectID)

	statusMessage := "Pushed changes to remote"
	if pushResult.CommitCreated {
		statusMessage = "Committed and pushed changes"
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]any{
		"status":        statusMessage,
		"commitCreated": pushResult.CommitCreated,
		"commitHash":    pushResult.CommitHash,
	})
}

func (s *Server) handleGitReset(w http.ResponseWriter, r *http.Request) {
	access, ok := s.requireProjectAccess(w, r, projectAccessWrite)
	if !ok {
		return
	}

	cfg, err := s.getProjectGitConfig(access.ProjectID)
	if err == sql.ErrNoRows {
		http.Error(w, "Git sync is not configured for this project", http.StatusNotFound)
		return
	}
	if err != nil {
		http.Error(w, "Failed to load Git configuration", http.StatusInternalServerError)
		return
	}
	_ = cfg

	if err := s.git.Reset(r.Context(), filepath.Join(s.projectsDir, access.ProjectID)); err != nil {
		http.Error(w, renderGitError(err), http.StatusBadRequest)
		return
	}

	s.touchProject(access.ProjectID)

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]string{
		"status": "Discarded all local changes",
	})
}

func (s *Server) getProjectGitConfig(projectID string) (projectGitConfigRecord, error) {
	var cfg projectGitConfigRecord
	err := s.db.QueryRow(
		`SELECT project_id, remote_url, branch, COALESCE(last_sync, ''), created, updated
		FROM project_git_configs
		WHERE project_id = ?`,
		projectID,
	).Scan(
		&cfg.ProjectID,
		&cfg.RemoteURL,
		&cfg.Branch,
		&cfg.LastSync,
		&cfg.Created,
		&cfg.Updated,
	)
	return cfg, err
}

func (s *Server) getUserGitKey(userID string) (userGitKeyRecord, error) {
	var key userGitKeyRecord
	err := s.db.QueryRow(
		`SELECT user_id, public_key, private_key_encrypted, fingerprint, created, updated
		FROM user_git_keys
		WHERE user_id = ?`,
		userID,
	).Scan(
		&key.UserID,
		&key.PublicKey,
		&key.PrivateKeyEncrypted,
		&key.Fingerprint,
		&key.Created,
		&key.Updated,
	)
	return key, err
}

func (s *Server) getUserGitKeySummary(userID string) (*UserGitKeySummary, error) {
	key, err := s.getUserGitKey(userID)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}

	return &UserGitKeySummary{
		UserID:      key.UserID,
		PublicKey:   key.PublicKey,
		Fingerprint: key.Fingerprint,
		Created:     key.Created,
		Updated:     key.Updated,
	}, nil
}

// errUserGitKeyUndecryptable means the stored SSH private key could not be
// decrypted. The key is sealed with a key derived from JWT_SECRET, so this
// almost always means JWT_SECRET changed since the key was generated.
var errUserGitKeyUndecryptable = errors.New(
	"your saved SSH key could not be decrypted, which usually means JWT_SECRET changed since it was generated. " +
		"Generate a new key on the Settings page and add it to your Git host, or restore the previous JWT_SECRET.",
)

func gitKeyLoadErrorMessage(err error) string {
	if errors.Is(err, errUserGitKeyUndecryptable) {
		return errUserGitKeyUndecryptable.Error()
	}
	return "Failed to load your SSH key"
}

func (s *Server) getOptionalUserGitAuth(userID string) (userGitKeyRecord, gitclient.Auth, error) {
	key, err := s.getUserGitKey(userID)
	if err == sql.ErrNoRows {
		return userGitKeyRecord{}, gitclient.Auth{}, nil
	}
	if err != nil {
		return userGitKeyRecord{}, gitclient.Auth{}, err
	}

	privateKey, err := gitclient.DecryptSecret(s.jwtSecret, key.PrivateKeyEncrypted)
	if err != nil {
		return userGitKeyRecord{}, gitclient.Auth{}, errUserGitKeyUndecryptable
	}

	return key, gitclient.Auth{SSHPrivateKey: privateKey}, nil
}

func (s *Server) upsertProjectGitConfig(cfg projectGitConfigRecord) error {
	_, err := s.db.Exec(
		`INSERT INTO project_git_configs (project_id, remote_url, branch)
		VALUES (?, ?, ?)
		ON CONFLICT(project_id)
		DO UPDATE SET
			remote_url = excluded.remote_url,
			branch = excluded.branch,
			updated = datetime('now')`,
		cfg.ProjectID, cfg.RemoteURL, cfg.Branch,
	)
	return err
}

func (s *Server) upsertUserGitKey(key userGitKeyRecord) error {
	_, err := s.db.Exec(
		`INSERT INTO user_git_keys (user_id, public_key, private_key_encrypted, fingerprint)
		VALUES (?, ?, ?, ?)
		ON CONFLICT(user_id)
		DO UPDATE SET
			public_key = excluded.public_key,
			private_key_encrypted = excluded.private_key_encrypted,
			fingerprint = excluded.fingerprint,
			updated = datetime('now')`,
		key.UserID, key.PublicKey, key.PrivateKeyEncrypted, key.Fingerprint,
	)
	return err
}

func (s *Server) updateProjectGitLastSync(projectID string) {
	s.db.Exec(
		"UPDATE project_git_configs SET last_sync = datetime('now'), updated = datetime('now') WHERE project_id = ?",
		projectID,
	)
}

func defaultProjectNameFromRemote(remoteURL string) string {
	trimmed := strings.TrimSpace(strings.TrimSuffix(remoteURL, "/"))
	if trimmed == "" {
		return "Cloned Project"
	}

	slashIndex := strings.LastIndex(trimmed, "/")
	if slashIndex >= 0 {
		trimmed = trimmed[slashIndex+1:]
	} else if colonIndex := strings.LastIndex(trimmed, ":"); colonIndex >= 0 {
		trimmed = trimmed[colonIndex+1:]
	}

	trimmed = strings.TrimSuffix(trimmed, ".git")
	trimmed = strings.TrimSpace(trimmed)
	if trimmed == "" {
		return "Cloned Project"
	}
	return trimmed
}

func normalizeGitBranch(input string, allowEmpty bool) (string, error) {
	value := strings.TrimSpace(input)
	if value == "" {
		if allowEmpty {
			return "", nil
		}
		return "", errors.New("branch is required")
	}

	if value == "HEAD" || strings.HasPrefix(value, "/") || strings.HasSuffix(value, "/") || strings.HasSuffix(value, ".") || strings.HasSuffix(value, ".lock") {
		return "", errors.New("invalid Git branch name")
	}
	if strings.HasPrefix(value, "-") || strings.Contains(value, "..") || strings.Contains(value, "//") || strings.Contains(value, "@{") {
		return "", errors.New("invalid Git branch name")
	}

	for _, r := range value {
		if unicode.IsSpace(r) || r < 32 || r == 127 {
			return "", errors.New("invalid Git branch name")
		}
		switch r {
		case '\\', '~', '^', ':', '?', '*', '[':
			return "", errors.New("invalid Git branch name")
		}
	}

	return value, nil
}

func usesSSHRemote(remoteURL string) bool {
	value := strings.ToLower(strings.TrimSpace(remoteURL))
	if strings.HasPrefix(value, "ssh://") {
		return true
	}
	return strings.Contains(value, "@") && strings.Contains(value, ":") && !strings.Contains(value, "://")
}

func ensureUserGitKeyForRemote(remoteURL string, auth gitclient.Auth) error {
	if !usesSSHRemote(remoteURL) {
		return nil
	}
	if strings.TrimSpace(auth.SSHPrivateKey) != "" {
		return nil
	}
	return errors.New("generate your user SSH key before using an SSH remote")
}

func userGitFingerprint(summary *UserGitKeySummary) string {
	if summary == nil {
		return ""
	}
	return summary.Fingerprint
}

func renderGitError(err error) string {
	if err == nil {
		return ""
	}

	var cmdErr *gitclient.CommandError
	if errors.As(err, &cmdErr) && strings.TrimSpace(cmdErr.Output) != "" {
		return strings.TrimSpace(cmdErr.Output)
	}

	return strings.TrimSpace(err.Error())
}
