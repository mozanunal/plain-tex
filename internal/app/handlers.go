package app

import (
	"archive/zip"
	"database/sql"
	"encoding/json"
	"errors"
	"io"
	"mime"
	"net/http"
	"net/url"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
)

type PageData struct {
	User                 *User
	Users                []UserSummary
	UserGitKey           *UserGitKeySummary
	Projects             []Project
	Project              *Project
	ProjectMembers       []ProjectMember
	CompileTarget        string
	Content              string
	Error                string
	Status               string
	CanWrite             bool
	CanComment           bool
	CanManageMembers     bool
	RegistrationDisabled bool
	ActiveNav            string
	AdminCount           int
	AccountCreated       string
}

type Project struct {
	ID           string
	UserID       string
	Name         string
	CompileEntry string
	Created      string
	Updated      string
	AccessRole   string
	OwnerEmail   string
}

type UserSummary struct {
	ID      string
	Email   string
	Name    string
	IsAdmin bool
}

type UserGitKeySummary struct {
	UserID      string
	PublicKey   string
	Fingerprint string
	Created     string
	Updated     string
}

type ProjectMember struct {
	UserID  string
	Email   string
	Name    string
	Role    string
	IsOwner bool
}

type projectAccessLevel int

const (
	projectAccessRead projectAccessLevel = iota
	projectAccessWrite
	projectAccessManage
)

const (
	projectRoleOwner     = "owner"
	projectRoleReader    = "reader"
	projectRoleCommenter = "commenter"
	projectRoleWriter    = "writer"
	projectRoleAdmin     = "admin"
)

type ProjectAccess struct {
	ProjectID        string
	OwnerID          string
	MemberRole       string
	EffectiveRole    string
	CanRead          bool
	CanWrite         bool
	CanManageMembers bool
	CanDeleteProject bool
}

func (s *Server) handleHealth(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")

	if err := s.db.PingContext(r.Context()); err != nil {
		w.WriteHeader(http.StatusServiceUnavailable)
		json.NewEncoder(w).Encode(map[string]string{
			"status": "unavailable",
			"error":  "database unreachable",
		})
		return
	}

	json.NewEncoder(w).Encode(map[string]string{"status": "ok"})
}

func (s *Server) handleLoginPage(w http.ResponseWriter, r *http.Request) {
	var userCount int
	if err := s.db.QueryRow("SELECT COUNT(*) FROM users").Scan(&userCount); err != nil {
		http.Error(w, "Failed to load login page", http.StatusInternalServerError)
		return
	}

	s.templates.ExecuteTemplate(w, "login.html", PageData{
		RegistrationDisabled: userCount > 0,
	})
}

func (s *Server) handleRegisterPage(w http.ResponseWriter, r *http.Request) {
	var userCount int
	if err := s.db.QueryRow("SELECT COUNT(*) FROM users").Scan(&userCount); err != nil {
		http.Error(w, "Failed to load register page", http.StatusInternalServerError)
		return
	}

	if userCount > 0 {
		s.templates.ExecuteTemplate(w, "register.html", PageData{
			Error:                "Registration is disabled. Ask an admin to create your account.",
			RegistrationDisabled: true,
		})
		return
	}

	s.templates.ExecuteTemplate(w, "register.html", nil)
}

func (s *Server) handleLogin(w http.ResponseWriter, r *http.Request) {
	email := strings.ToLower(strings.TrimSpace(r.FormValue("email")))
	password := r.FormValue("password")
	registrationDisabled := true
	var userCount int
	if err := s.db.QueryRow("SELECT COUNT(*) FROM users").Scan(&userCount); err == nil {
		registrationDisabled = userCount > 0
	}

	var user struct {
		ID           string
		Email        string
		PasswordHash string
		Name         sql.NullString
	}

	err := s.db.QueryRow(
		"SELECT id, email, password_hash, name FROM users WHERE email = ?",
		email,
	).Scan(&user.ID, &user.Email, &user.PasswordHash, &user.Name)

	if err != nil || !checkPassword(password, user.PasswordHash) {
		s.templates.ExecuteTemplate(w, "login.html", PageData{
			Error:                "Invalid credentials",
			RegistrationDisabled: registrationDisabled,
		})
		return
	}

	token, err := s.createToken(user.ID, user.Email)
	if err != nil {
		s.templates.ExecuteTemplate(w, "login.html", PageData{
			Error:                "Login failed",
			RegistrationDisabled: registrationDisabled,
		})
		return
	}

	http.SetCookie(w, &http.Cookie{
		Name:     "token",
		Value:    token,
		Path:     "/",
		HttpOnly: true,
		SameSite: http.SameSiteStrictMode,
	})

	http.Redirect(w, r, "/", http.StatusSeeOther)
}

func (s *Server) handleRegister(w http.ResponseWriter, r *http.Request) {
	email := strings.ToLower(strings.TrimSpace(r.FormValue("email")))
	password := r.FormValue("password")
	name := r.FormValue("name")

	var userCount int
	if err := s.db.QueryRow("SELECT COUNT(*) FROM users").Scan(&userCount); err != nil {
		s.templates.ExecuteTemplate(w, "register.html", PageData{Error: "Registration failed"})
		return
	}
	if userCount > 0 {
		s.templates.ExecuteTemplate(w, "register.html", PageData{
			Error:                "Registration is disabled. Ask an admin to create your account.",
			RegistrationDisabled: true,
		})
		return
	}
	isAdmin := 0
	if userCount == 0 {
		isAdmin = 1
	}

	hash, err := hashPassword(password)
	if err != nil {
		s.templates.ExecuteTemplate(w, "register.html", PageData{Error: "Registration failed"})
		return
	}

	id := uuid.New().String()
	_, err = s.db.Exec(
		"INSERT INTO users (id, email, password_hash, name, is_admin) VALUES (?, ?, ?, ?, ?)",
		id, email, hash, name, isAdmin,
	)

	if err != nil {
		s.templates.ExecuteTemplate(w, "register.html", PageData{Error: "Email already exists"})
		return
	}

	token, err := s.createToken(id, email)
	if err != nil {
		s.templates.ExecuteTemplate(w, "register.html", PageData{Error: "Registration failed"})
		return
	}

	http.SetCookie(w, &http.Cookie{
		Name:     "token",
		Value:    token,
		Path:     "/",
		HttpOnly: true,
		SameSite: http.SameSiteStrictMode,
	})

	http.Redirect(w, r, "/", http.StatusSeeOther)
}

func (s *Server) handleLogout(w http.ResponseWriter, r *http.Request) {
	http.SetCookie(w, &http.Cookie{
		Name:     "token",
		Value:    "",
		Path:     "/",
		MaxAge:   -1,
		HttpOnly: true,
	})

	http.Redirect(w, r, "/login", http.StatusSeeOther)
}

func (s *Server) handleProjectsPage(w http.ResponseWriter, r *http.Request) {
	user := getUserFromContext(r.Context())

	rows, err := s.db.Query(
		`
		SELECT p.id, p.user_id, p.name, p.created, p.updated,
			CASE
				WHEN p.user_id = ? THEN ?
				ELSE COALESCE(pm.role, '')
			END AS access_role,
			owner.email
		FROM projects p
		JOIN users owner ON owner.id = p.user_id
		LEFT JOIN project_members pm ON pm.project_id = p.id AND pm.user_id = ?
		WHERE p.user_id = ? OR pm.user_id = ?
		ORDER BY p.updated DESC
		`,
		user.ID, projectRoleOwner, user.ID, user.ID, user.ID,
	)
	if err != nil {
		http.Error(w, "Failed to load projects", http.StatusInternalServerError)
		return
	}
	defer rows.Close()

	var projects []Project
	for rows.Next() {
		var p Project
		if err := rows.Scan(&p.ID, &p.UserID, &p.Name, &p.Created, &p.Updated, &p.AccessRole, &p.OwnerEmail); err != nil {
			http.Error(w, "Failed to load projects", http.StatusInternalServerError)
			return
		}
		projects = append(projects, p)
	}
	if err := rows.Err(); err != nil {
		http.Error(w, "Failed to load projects", http.StatusInternalServerError)
		return
	}

	pageData := PageData{
		User:      user,
		Projects:  projects,
		ActiveNav: "home",
		Status:    strings.TrimSpace(r.URL.Query().Get("status")),
		Error:     strings.TrimSpace(r.URL.Query().Get("error")),
	}

	userGitKey, err := s.getUserGitKeySummary(user.ID)
	if err != nil {
		http.Error(w, "Failed to load Git key", http.StatusInternalServerError)
		return
	}
	pageData.UserGitKey = userGitKey

	s.templates.ExecuteTemplate(w, "projects.html", pageData)
}

func (s *Server) handleSettingsPage(w http.ResponseWriter, r *http.Request) {
	user := getUserFromContext(r.Context())

	userGitKey, err := s.getUserGitKeySummary(user.ID)
	if err != nil {
		http.Error(w, "Failed to load Git key", http.StatusInternalServerError)
		return
	}

	var created string
	if err := s.db.QueryRow("SELECT created FROM users WHERE id = ?", user.ID).Scan(&created); err != nil {
		http.Error(w, "Failed to load account", http.StatusInternalServerError)
		return
	}

	s.templates.ExecuteTemplate(w, "settings.html", PageData{
		User:           user,
		UserGitKey:     userGitKey,
		AccountCreated: created,
		ActiveNav:      "settings",
		Status:         strings.TrimSpace(r.URL.Query().Get("status")),
		Error:          strings.TrimSpace(r.URL.Query().Get("error")),
	})
}

func (s *Server) handleUpdateProfile(w http.ResponseWriter, r *http.Request) {
	user := getUserFromContext(r.Context())

	name := strings.TrimSpace(r.FormValue("name"))
	email := strings.ToLower(strings.TrimSpace(r.FormValue("email")))

	if email == "" {
		redirectWithMessage(w, r, "/settings", "", "Email is required")
		return
	}

	var exists bool
	if err := s.db.QueryRow(
		"SELECT EXISTS(SELECT 1 FROM users WHERE lower(email) = lower(?) AND id <> ?)",
		email, user.ID,
	).Scan(&exists); err != nil {
		http.Error(w, "Failed to update profile", http.StatusInternalServerError)
		return
	}
	if exists {
		redirectWithMessage(w, r, "/settings", "", "A user with this email already exists")
		return
	}

	if _, err := s.db.Exec(
		"UPDATE users SET name = ?, email = ?, updated = datetime('now') WHERE id = ?",
		name, email, user.ID,
	); err != nil {
		http.Error(w, "Failed to update profile", http.StatusInternalServerError)
		return
	}

	redirectWithMessage(w, r, "/settings", "Profile updated", "")
}

func (s *Server) handleChangePassword(w http.ResponseWriter, r *http.Request) {
	user := getUserFromContext(r.Context())

	currentPassword := r.FormValue("current_password")
	newPassword := r.FormValue("new_password")
	confirmPassword := r.FormValue("confirm_password")

	if newPassword != confirmPassword {
		redirectWithMessage(w, r, "/settings", "", "New passwords do not match")
		return
	}
	if len(newPassword) < 6 {
		redirectWithMessage(w, r, "/settings", "", "New password must be at least 6 characters")
		return
	}

	var hash string
	if err := s.db.QueryRow("SELECT password_hash FROM users WHERE id = ?", user.ID).Scan(&hash); err != nil {
		http.Error(w, "Failed to change password", http.StatusInternalServerError)
		return
	}
	if !checkPassword(currentPassword, hash) {
		redirectWithMessage(w, r, "/settings", "", "Current password is incorrect")
		return
	}

	newHash, err := hashPassword(newPassword)
	if err != nil {
		http.Error(w, "Failed to change password", http.StatusInternalServerError)
		return
	}
	if _, err := s.db.Exec(
		"UPDATE users SET password_hash = ?, updated = datetime('now') WHERE id = ?",
		newHash, user.ID,
	); err != nil {
		http.Error(w, "Failed to change password", http.StatusInternalServerError)
		return
	}

	redirectWithMessage(w, r, "/settings", "Password changed", "")
}

func (s *Server) handleAdminUsersPage(w http.ResponseWriter, r *http.Request) {
	user := getUserFromContext(r.Context())
	if user == nil || !user.IsAdmin {
		http.Error(w, "Forbidden", http.StatusForbidden)
		return
	}

	userRows, err := s.db.Query("SELECT id, email, name, is_admin FROM users ORDER BY email ASC")
	if err != nil {
		http.Error(w, "Failed to load users", http.StatusInternalServerError)
		return
	}
	defer userRows.Close()

	var users []UserSummary
	for userRows.Next() {
		var candidate UserSummary
		var name sql.NullString
		var isAdmin int
		if err := userRows.Scan(&candidate.ID, &candidate.Email, &name, &isAdmin); err != nil {
			http.Error(w, "Failed to load users", http.StatusInternalServerError)
			return
		}
		if name.Valid {
			candidate.Name = name.String
		}
		candidate.IsAdmin = isAdmin == 1
		users = append(users, candidate)
	}
	if err := userRows.Err(); err != nil {
		http.Error(w, "Failed to load users", http.StatusInternalServerError)
		return
	}

	adminCount := 0
	for _, candidate := range users {
		if candidate.IsAdmin {
			adminCount++
		}
	}

	s.templates.ExecuteTemplate(w, "admin_users.html", PageData{
		User:       user,
		Users:      users,
		AdminCount: adminCount,
		ActiveNav:  "admin",
		Status:     strings.TrimSpace(r.URL.Query().Get("status")),
		Error:      strings.TrimSpace(r.URL.Query().Get("error")),
	})
}

func (s *Server) handleCreateProject(w http.ResponseWriter, r *http.Request) {
	user := getUserFromContext(r.Context())
	name := r.FormValue("name")

	if name == "" {
		name = "Untitled Project"
	}

	id := uuid.New().String()
	_, err := s.db.Exec(
		"INSERT INTO projects (id, user_id, name) VALUES (?, ?, ?)",
		id, user.ID, name,
	)

	if err != nil {
		http.Error(w, "Failed to create project", http.StatusInternalServerError)
		return
	}

	projectDir := filepath.Join(s.projectsDir, id)
	os.MkdirAll(projectDir, 0755)

	defaultTex := `\documentclass{article}
\begin{document}
Hello, World!
\end{document}
`
	os.WriteFile(filepath.Join(projectDir, "main.tex"), []byte(defaultTex), 0644)

	http.Redirect(w, r, "/editor/"+id, http.StatusSeeOther)
}

func (s *Server) handleAdminCreateUser(w http.ResponseWriter, r *http.Request) {
	user := getUserFromContext(r.Context())
	if user == nil || !user.IsAdmin {
		http.Error(w, "Forbidden", http.StatusForbidden)
		return
	}

	email := strings.ToLower(strings.TrimSpace(r.FormValue("email")))
	password := r.FormValue("password")
	name := strings.TrimSpace(r.FormValue("name"))

	if email == "" || password == "" {
		redirectWithMessage(w, r, "/admin/users", "", "Email and password are required")
		return
	}
	if len(password) < 6 {
		redirectWithMessage(w, r, "/admin/users", "", "Password must be at least 6 characters")
		return
	}

	var exists bool
	if err := s.db.QueryRow("SELECT EXISTS(SELECT 1 FROM users WHERE lower(email) = lower(?))", email).Scan(&exists); err != nil {
		http.Error(w, "Failed to check user", http.StatusInternalServerError)
		return
	}
	if exists {
		redirectWithMessage(w, r, "/admin/users", "", "A user with this email already exists")
		return
	}

	hash, err := hashPassword(password)
	if err != nil {
		http.Error(w, "Failed to create user", http.StatusInternalServerError)
		return
	}

	isAdmin := 0
	if r.FormValue("is_admin") == "1" {
		isAdmin = 1
	}

	_, err = s.db.Exec(
		"INSERT INTO users (id, email, password_hash, name, is_admin) VALUES (?, ?, ?, ?, ?)",
		uuid.New().String(), email, hash, name, isAdmin,
	)
	if err != nil {
		http.Error(w, "Failed to create user", http.StatusInternalServerError)
		return
	}

	redirectWithMessage(w, r, "/admin/users", "User created", "")
}

func (s *Server) handleAdminResetPassword(w http.ResponseWriter, r *http.Request) {
	user := getUserFromContext(r.Context())
	if user == nil || !user.IsAdmin {
		http.Error(w, "Forbidden", http.StatusForbidden)
		return
	}

	targetID := strings.TrimSpace(chi.URLParam(r, "userID"))
	newPassword := r.FormValue("password")

	if len(newPassword) < 6 {
		redirectWithMessage(w, r, "/admin/users", "", "Password must be at least 6 characters")
		return
	}

	var email string
	err := s.db.QueryRow("SELECT email FROM users WHERE id = ?", targetID).Scan(&email)
	if err == sql.ErrNoRows {
		redirectWithMessage(w, r, "/admin/users", "", "User not found")
		return
	}
	if err != nil {
		http.Error(w, "Failed to reset password", http.StatusInternalServerError)
		return
	}

	hash, err := hashPassword(newPassword)
	if err != nil {
		http.Error(w, "Failed to reset password", http.StatusInternalServerError)
		return
	}

	if _, err := s.db.Exec(
		"UPDATE users SET password_hash = ?, updated = datetime('now') WHERE id = ?",
		hash, targetID,
	); err != nil {
		http.Error(w, "Failed to reset password", http.StatusInternalServerError)
		return
	}

	redirectWithMessage(w, r, "/admin/users", "Password reset for "+email, "")
}

func (s *Server) handleAddProjectMember(w http.ResponseWriter, r *http.Request) {
	access, ok := s.requireProjectAccess(w, r, projectAccessManage)
	if !ok {
		return
	}

	email := strings.ToLower(strings.TrimSpace(r.FormValue("email")))
	role, roleValid := normalizeMemberRole(r.FormValue("role"))
	if email == "" || !roleValid {
		redirectWithMessage(w, r, "/editor/"+access.ProjectID, "", "Valid email and role are required")
		return
	}

	var memberID string
	err := s.db.QueryRow("SELECT id FROM users WHERE lower(email) = lower(?)", email).Scan(&memberID)
	if err == sql.ErrNoRows {
		redirectWithMessage(w, r, "/editor/"+access.ProjectID, "", "User not found")
		return
	}
	if err != nil {
		http.Error(w, "Failed to add project member", http.StatusInternalServerError)
		return
	}

	if memberID == access.OwnerID {
		redirectWithMessage(w, r, "/editor/"+access.ProjectID, "", "Owner already has full access")
		return
	}

	_, err = s.db.Exec(
		`INSERT INTO project_members (project_id, user_id, role)
		VALUES (?, ?, ?)
		ON CONFLICT(project_id, user_id)
		DO UPDATE SET role = excluded.role, updated = datetime('now')`,
		access.ProjectID, memberID, role,
	)
	if err != nil {
		http.Error(w, "Failed to add project member", http.StatusInternalServerError)
		return
	}

	redirectWithMessage(w, r, "/editor/"+access.ProjectID, "Member access updated", "")
}

func (s *Server) handleRemoveProjectMember(w http.ResponseWriter, r *http.Request) {
	access, ok := s.requireProjectAccess(w, r, projectAccessManage)
	if !ok {
		return
	}

	memberID := strings.TrimSpace(chi.URLParam(r, "userID"))
	if memberID == "" {
		redirectWithMessage(w, r, "/editor/"+access.ProjectID, "", "Member ID is required")
		return
	}
	if memberID == access.OwnerID {
		redirectWithMessage(w, r, "/editor/"+access.ProjectID, "", "Owner cannot be removed")
		return
	}

	_, err := s.db.Exec(
		"DELETE FROM project_members WHERE project_id = ? AND user_id = ?",
		access.ProjectID, memberID,
	)
	if err != nil {
		http.Error(w, "Failed to remove project member", http.StatusInternalServerError)
		return
	}

	redirectWithMessage(w, r, "/editor/"+access.ProjectID, "Member removed", "")
}

func (s *Server) handleDeleteProject(w http.ResponseWriter, r *http.Request) {
	access, ok := s.requireProjectAccess(w, r, projectAccessManage)
	if !ok {
		return
	}

	_, err := s.db.Exec("DELETE FROM project_members WHERE project_id = ?", access.ProjectID)
	if err != nil {
		http.Error(w, "Failed to delete project", http.StatusInternalServerError)
		return
	}
	if _, err := s.db.Exec("DELETE FROM project_git_configs WHERE project_id = ?", access.ProjectID); err != nil {
		http.Error(w, "Failed to delete project", http.StatusInternalServerError)
		return
	}

	result, err := s.db.Exec(
		"DELETE FROM projects WHERE id = ?",
		access.ProjectID,
	)
	if err != nil {
		http.Error(w, "Failed to delete project", http.StatusInternalServerError)
		return
	}

	affected, _ := result.RowsAffected()
	if affected == 0 {
		http.Error(w, "Project not found", http.StatusNotFound)
		return
	}

	s.db.Exec("DELETE FROM comments WHERE project_id = ?", access.ProjectID)

	projectDir := filepath.Join(s.projectsDir, access.ProjectID)
	os.RemoveAll(projectDir)

	w.WriteHeader(http.StatusOK)
}

func (s *Server) handleEditorPage(w http.ResponseWriter, r *http.Request) {
	user := getUserFromContext(r.Context())
	projectID := chi.URLParam(r, "id")

	access, err := s.getProjectAccess(user, projectID)
	if err == sql.ErrNoRows || !access.CanRead {
		http.Redirect(w, r, "/", http.StatusSeeOther)
		return
	}
	if err != nil {
		http.Error(w, "Failed to load project", http.StatusInternalServerError)
		return
	}

	var project Project
	err = s.db.QueryRow(
		`SELECT p.id, p.user_id, p.name, p.compile_entry, p.created, p.updated, owner.email
		FROM projects p
		JOIN users owner ON owner.id = p.user_id
		WHERE p.id = ?`,
		projectID,
	).Scan(&project.ID, &project.UserID, &project.Name, &project.CompileEntry, &project.Created, &project.Updated, &project.OwnerEmail)
	if err != nil {
		http.Error(w, "Failed to load project", http.StatusInternalServerError)
		return
	}
	project.AccessRole = access.EffectiveRole

	projectDir := filepath.Join(s.projectsDir, projectID)
	compileTarget, err := s.preferredCompileEntry(projectID, projectDir)
	if err != nil {
		http.Error(w, "Failed to load compile target", http.StatusInternalServerError)
		return
	}

	content := []byte{}
	if compileTarget != "" {
		if _, targetPath, resolveErr := resolveProjectPath(projectDir, compileTarget, false); resolveErr == nil {
			content, _ = os.ReadFile(targetPath)
		}
	}

	members, err := s.listProjectMembers(projectID)
	if err != nil {
		http.Error(w, "Failed to load project members", http.StatusInternalServerError)
		return
	}

	userGitKey, err := s.getUserGitKeySummary(user.ID)
	if err != nil {
		http.Error(w, "Failed to load Git key", http.StatusInternalServerError)
		return
	}

	s.templates.ExecuteTemplate(w, "editor.html", PageData{
		User:             user,
		UserGitKey:       userGitKey,
		Project:          &project,
		ProjectMembers:   members,
		CompileTarget:    compileTarget,
		Content:          string(content),
		Status:           strings.TrimSpace(r.URL.Query().Get("status")),
		Error:            strings.TrimSpace(r.URL.Query().Get("error")),
		CanWrite:         access.CanWrite,
		CanComment:       canCommentOnProject(access),
		CanManageMembers: access.CanManageMembers,
	})
}

func (s *Server) handleCompile(w http.ResponseWriter, r *http.Request) {
	access, ok := s.requireProjectAccess(w, r, projectAccessWrite)
	if !ok {
		return
	}
	projectID := access.ProjectID

	projectDir := filepath.Join(s.projectsDir, projectID)

	entry := strings.TrimSpace(r.FormValue("entry"))
	if entry == "" {
		defaultEntry, err := s.preferredCompileEntry(projectID, projectDir)
		if err != nil {
			http.Error(w, "Failed to resolve compile entry", http.StatusInternalServerError)
			return
		}
		if defaultEntry == "" {
			http.Error(w, errNoCompileEntry.Error(), http.StatusBadRequest)
			return
		}
		entry = defaultEntry
	} else {
		normalizedEntry, _, err := resolveProjectPath(projectDir, entry, false)
		if err != nil {
			http.Error(w, "Invalid compile entry", http.StatusBadRequest)
			return
		}
		entry = normalizedEntry
	}

	if !isCompilableSource(entry) {
		http.Error(w, "Compile entry must be a .tex or .typ file", http.StatusBadRequest)
		return
	}

	entryPath := filepath.Join(projectDir, filepath.FromSlash(entry))
	entryInfo, err := os.Stat(entryPath)
	if os.IsNotExist(err) {
		http.Error(w, "Compile entry file not found", http.StatusBadRequest)
		return
	}
	if err != nil {
		http.Error(w, "Failed to access compile entry", http.StatusInternalServerError)
		return
	}
	if entryInfo.IsDir() {
		http.Error(w, "Compile entry must be a file", http.StatusBadRequest)
		return
	}

	if err := s.setProjectCompileEntry(projectID, entry); err != nil {
		http.Error(w, "Failed to save compile target", http.StatusInternalServerError)
		return
	}

	compileStartedAt := time.Now()
	pdf, output, err := s.compiler.Compile(projectDir, entry)
	compileDurationMs := time.Since(compileStartedAt).Milliseconds()
	w.Header().Set("X-Compile-Ms", strconv.FormatInt(compileDurationMs, 10))
	w.Header().Set("X-Latex-Compile-Ms", strconv.FormatInt(compileDurationMs, 10))
	w.Header().Set("X-Compile-Entry", entry)
	if err != nil {
		w.Header().Set("Content-Type", "text/plain")
		w.WriteHeader(http.StatusBadRequest)
		w.Write([]byte(output))
		return
	}

	s.db.Exec("UPDATE projects SET updated = datetime('now') WHERE id = ?", projectID)

	w.Header().Set("Content-Type", "application/pdf")
	w.Write(pdf)
}

func (s *Server) handleUpdateCompileTarget(w http.ResponseWriter, r *http.Request) {
	access, ok := s.requireProjectAccess(w, r, projectAccessWrite)
	if !ok {
		return
	}

	entry := strings.TrimSpace(r.FormValue("entry"))
	if entry == "" {
		if err := s.setProjectCompileEntry(access.ProjectID, ""); err != nil {
			http.Error(w, "Failed to clear compile target", http.StatusInternalServerError)
			return
		}
		w.WriteHeader(http.StatusNoContent)
		return
	}

	projectDir := filepath.Join(s.projectsDir, access.ProjectID)
	normalizedEntry, entryPath, err := resolveProjectPath(projectDir, entry, false)
	if err != nil {
		http.Error(w, "Invalid compile entry", http.StatusBadRequest)
		return
	}
	if !isCompilableSource(normalizedEntry) {
		http.Error(w, "Compile entry must be a .tex, .typ, or .md file", http.StatusBadRequest)
		return
	}

	entryInfo, err := os.Stat(entryPath)
	if os.IsNotExist(err) {
		http.Error(w, "Compile entry file not found", http.StatusBadRequest)
		return
	}
	if err != nil {
		http.Error(w, "Failed to access compile entry", http.StatusInternalServerError)
		return
	}
	if entryInfo.IsDir() {
		http.Error(w, "Compile entry must be a file", http.StatusBadRequest)
		return
	}

	if err := s.setProjectCompileEntry(access.ProjectID, normalizedEntry); err != nil {
		http.Error(w, "Failed to save compile target", http.StatusInternalServerError)
		return
	}

	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) handleSave(w http.ResponseWriter, r *http.Request) {
	access, ok := s.requireProjectAccess(w, r, projectAccessWrite)
	if !ok {
		return
	}
	projectID := access.ProjectID

	content := r.FormValue("content")
	texPath := filepath.Join(s.projectsDir, projectID, "main.tex")

	if err := os.WriteFile(texPath, []byte(content), 0644); err != nil {
		http.Error(w, "Failed to save", http.StatusInternalServerError)
		return
	}

	s.db.Exec("UPDATE projects SET updated = datetime('now') WHERE id = ?", projectID)

	w.WriteHeader(http.StatusOK)
}

func (s *Server) handleGetPDF(w http.ResponseWriter, r *http.Request) {
	access, ok := s.requireProjectAccess(w, r, projectAccessRead)
	if !ok {
		return
	}
	projectID := access.ProjectID

	pdfPath := filepath.Join(s.projectsDir, projectID, "main.pdf")
	pdf, err := os.ReadFile(pdfPath)
	if err != nil {
		http.Error(w, "PDF not found", http.StatusNotFound)
		return
	}

	w.Header().Set("Content-Type", "application/pdf")
	w.Write(pdf)
}

func sanitizeDownloadBaseName(name, fallback string) string {
	base := strings.TrimSpace(name)
	if base == "" {
		base = fallback
	}
	if base == "" {
		base = "project"
	}

	var builder strings.Builder
	lastDash := false
	for _, r := range base {
		switch {
		case (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9'):
			builder.WriteRune(r)
			lastDash = false
		case r == '-' || r == '_' || r == '.':
			builder.WriteRune(r)
			lastDash = false
		case r == ' ':
			if !lastDash && builder.Len() > 0 {
				builder.WriteByte('-')
				lastDash = true
			}
		}
	}

	sanitized := strings.Trim(builder.String(), "-._")
	if sanitized == "" {
		return "project"
	}
	return sanitized
}

func (s *Server) projectDownloadBaseName(projectID string) string {
	var projectName string
	err := s.db.QueryRow("SELECT name FROM projects WHERE id = ?", projectID).Scan(&projectName)
	if err == nil {
		return sanitizeDownloadBaseName(projectName, projectID)
	}
	return sanitizeDownloadBaseName(projectID, "project")
}

func (s *Server) handleDownloadPDF(w http.ResponseWriter, r *http.Request) {
	access, ok := s.requireProjectAccess(w, r, projectAccessRead)
	if !ok {
		return
	}
	projectID := access.ProjectID

	pdfPath := filepath.Join(s.projectsDir, projectID, "main.pdf")
	pdf, err := os.ReadFile(pdfPath)
	if err != nil {
		http.Error(w, "PDF not found. Compile first.", http.StatusNotFound)
		return
	}

	baseName := s.projectDownloadBaseName(projectID)
	w.Header().Set("Content-Type", "application/pdf")
	w.Header().Set("Content-Disposition", "attachment; filename=\""+baseName+".pdf\"")
	w.Write(pdf)
}

func (s *Server) handleDownloadSource(w http.ResponseWriter, r *http.Request) {
	access, ok := s.requireProjectAccess(w, r, projectAccessRead)
	if !ok {
		return
	}
	projectID := access.ProjectID
	projectDir := filepath.Join(s.projectsDir, projectID)

	filePaths := make([]string, 0, 32)
	err := filepath.WalkDir(projectDir, func(currentPath string, entry os.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if currentPath == projectDir {
			return nil
		}

		relPath, err := filepath.Rel(projectDir, currentPath)
		if err != nil {
			return err
		}
		relPath = filepath.ToSlash(relPath)

		if shouldSkipFileListEntry(relPath, entry.IsDir()) {
			if entry.IsDir() {
				return filepath.SkipDir
			}
			return nil
		}

		if entry.IsDir() {
			return nil
		}

		filePaths = append(filePaths, relPath)
		return nil
	})
	if err != nil {
		http.Error(w, "Failed to build source archive", http.StatusInternalServerError)
		return
	}

	baseName := s.projectDownloadBaseName(projectID)
	w.Header().Set("Content-Type", "application/zip")
	w.Header().Set("Content-Disposition", "attachment; filename=\""+baseName+"-source.zip\"")

	zipWriter := zip.NewWriter(w)
	for _, relPath := range filePaths {
		absPath := filepath.Join(projectDir, filepath.FromSlash(relPath))
		fileInfo, err := os.Stat(absPath)
		if err != nil {
			zipWriter.Close()
			return
		}

		header, err := zip.FileInfoHeader(fileInfo)
		if err != nil {
			zipWriter.Close()
			return
		}
		header.Name = relPath
		header.Method = zip.Deflate

		entryWriter, err := zipWriter.CreateHeader(header)
		if err != nil {
			zipWriter.Close()
			return
		}

		file, err := os.Open(absPath)
		if err != nil {
			zipWriter.Close()
			return
		}

		_, copyErr := io.Copy(entryWriter, file)
		closeErr := file.Close()
		if copyErr != nil || closeErr != nil {
			zipWriter.Close()
			return
		}
	}

	zipWriter.Close()
}

type FileInfo struct {
	Name        string `json:"name"`
	Path        string `json:"path"`
	IsDir       bool   `json:"isDir"`
	Size        int64  `json:"size"`
	IsText      bool   `json:"isText"`
	ContentType string `json:"contentType"`
}

type Comment struct {
	ID          string `json:"id"`
	ProjectID   string `json:"projectId"`
	FilePath    string `json:"filePath"`
	StartLine   int    `json:"startLine"`
	EndLine     int    `json:"endLine"`
	Body        string `json:"body"`
	Snippet     string `json:"snippet"`
	AuthorID    string `json:"authorId"`
	AuthorEmail string `json:"authorEmail"`
	Created     string `json:"created"`
	Updated     string `json:"updated"`
	CanDelete   bool   `json:"canDelete"`
}

var compilerArtifacts = map[string]struct{}{
	"main.pdf": {},
	"main.log": {},
	"main.aux": {},
}

var errNoCompileEntry = errors.New("no .tex, .typ, or .md entry file found")

func isCompilableSource(name string) bool {
	switch strings.ToLower(filepath.Ext(name)) {
	case ".tex", ".typ", ".md":
		return true
	default:
		return false
	}
}

func defaultCompileEntry(projectDir string) (string, error) {
	primaryCandidates := []string{"main.tex", "main.typ", "main.md", "README.md"}
	for _, candidate := range primaryCandidates {
		_, absPath, err := resolveProjectPath(projectDir, candidate, false)
		if err != nil {
			continue
		}
		info, statErr := os.Stat(absPath)
		if statErr == nil && !info.IsDir() {
			return candidate, nil
		}
		if statErr != nil && !os.IsNotExist(statErr) {
			return "", statErr
		}
	}

	var candidates []string
	err := filepath.WalkDir(projectDir, func(currentPath string, entry os.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if currentPath == projectDir {
			return nil
		}

		relPath, err := filepath.Rel(projectDir, currentPath)
		if err != nil {
			return err
		}
		relPath = filepath.ToSlash(relPath)

		if hasHiddenSegment(relPath) {
			if entry.IsDir() {
				return filepath.SkipDir
			}
			return nil
		}

		if entry.IsDir() {
			return nil
		}
		if isCompilableSource(relPath) {
			candidates = append(candidates, relPath)
		}
		return nil
	})
	if err != nil {
		return "", err
	}

	if len(candidates) == 0 {
		return "", errNoCompileEntry
	}

	sort.Strings(candidates)
	return candidates[0], nil
}

func isTextFile(name string) bool {
	ext := strings.ToLower(filepath.Ext(name))
	if ext == "" {
		return false
	}

	if detected := mime.TypeByExtension(ext); detected != "" {
		mediaType, _, err := mime.ParseMediaType(detected)
		if err == nil {
			if strings.HasPrefix(mediaType, "text/") {
				return true
			}
			switch mediaType {
			case "application/json", "application/javascript", "application/xml", "application/x-tex":
				return true
			}
		}
	}

	textExts := []string{
		".tex", ".ltx", ".typ", ".bib", ".sty", ".cls", ".txt", ".md", ".bst", ".cfg", ".def",
		".json", ".yaml", ".yml", ".xml", ".csv", ".tsv", ".html", ".htm", ".css", ".js", ".ts",
		".go", ".py", ".sh", ".toml", ".ini",
	}
	for _, candidate := range textExts {
		if ext == candidate {
			return true
		}
	}

	return false
}

func contentTypeForName(name string) string {
	ext := strings.ToLower(filepath.Ext(name))
	if ext == "" {
		if isTextFile(name) {
			return "text/plain; charset=utf-8"
		}
		return "application/octet-stream"
	}

	if detected := mime.TypeByExtension(ext); detected != "" {
		if strings.HasPrefix(strings.ToLower(detected), "text/") && !strings.Contains(strings.ToLower(detected), "charset=") {
			return detected + "; charset=utf-8"
		}
		return detected
	}

	if isTextFile(name) {
		return "text/plain; charset=utf-8"
	}

	return "application/octet-stream"
}

func detectContentType(name string, content []byte) string {
	byName := contentTypeForName(name)
	if byName != "application/octet-stream" {
		return byName
	}

	if len(content) == 0 {
		return byName
	}

	detected := http.DetectContentType(content)
	if strings.HasPrefix(strings.ToLower(detected), "text/") && !strings.Contains(strings.ToLower(detected), "charset=") {
		return detected + "; charset=utf-8"
	}
	return detected
}

func parseLineValue(value string) (int, error) {
	line, err := strconv.Atoi(strings.TrimSpace(value))
	if err != nil || line < 1 {
		return 0, errors.New("invalid line number")
	}
	return line, nil
}

func normalizeMemberRole(input string) (string, bool) {
	switch strings.ToLower(strings.TrimSpace(input)) {
	case projectRoleReader:
		return projectRoleReader, true
	case projectRoleCommenter:
		return projectRoleCommenter, true
	case projectRoleWriter:
		return projectRoleWriter, true
	default:
		return "", false
	}
}

func hasRequiredProjectAccess(access ProjectAccess, required projectAccessLevel) bool {
	switch required {
	case projectAccessRead:
		return access.CanRead
	case projectAccessWrite:
		return access.CanWrite
	case projectAccessManage:
		return access.CanManageMembers
	default:
		return false
	}
}

func canCommentOnProject(access ProjectAccess) bool {
	if access.CanWrite || access.CanManageMembers {
		return true
	}
	return access.EffectiveRole == projectRoleCommenter
}

func (s *Server) getProjectAccess(user *User, projectID string) (ProjectAccess, error) {
	var ownerID string
	var memberRole sql.NullString
	err := s.db.QueryRow(
		`SELECT p.user_id, pm.role
		FROM projects p
		LEFT JOIN project_members pm ON pm.project_id = p.id AND pm.user_id = ?
		WHERE p.id = ?`,
		user.ID, projectID,
	).Scan(&ownerID, &memberRole)
	if err != nil {
		return ProjectAccess{}, err
	}

	access := ProjectAccess{
		ProjectID: projectID,
		OwnerID:   ownerID,
	}
	if memberRole.Valid {
		access.MemberRole = memberRole.String
	}

	if user.IsAdmin {
		access.EffectiveRole = projectRoleAdmin
		access.CanRead = true
		access.CanWrite = true
		access.CanManageMembers = true
		access.CanDeleteProject = true
		return access, nil
	}

	if ownerID == user.ID {
		access.EffectiveRole = projectRoleOwner
		access.CanRead = true
		access.CanWrite = true
		access.CanManageMembers = true
		access.CanDeleteProject = true
		return access, nil
	}

	switch access.MemberRole {
	case projectRoleWriter:
		access.EffectiveRole = projectRoleWriter
		access.CanRead = true
		access.CanWrite = true
	case projectRoleCommenter:
		access.EffectiveRole = projectRoleCommenter
		access.CanRead = true
	case projectRoleReader:
		access.EffectiveRole = projectRoleReader
		access.CanRead = true
	}

	return access, nil
}

func (s *Server) requireProjectAccess(w http.ResponseWriter, r *http.Request, required projectAccessLevel) (ProjectAccess, bool) {
	user := getUserFromContext(r.Context())
	projectID := chi.URLParam(r, "id")

	access, err := s.getProjectAccess(user, projectID)
	if err == sql.ErrNoRows {
		http.Error(w, "Project not found", http.StatusNotFound)
		return ProjectAccess{}, false
	}
	if err != nil {
		http.Error(w, "Failed to load project permissions", http.StatusInternalServerError)
		return ProjectAccess{}, false
	}
	if !hasRequiredProjectAccess(access, required) {
		http.Error(w, "Forbidden", http.StatusForbidden)
		return ProjectAccess{}, false
	}

	return access, true
}

func (s *Server) listProjectMembers(projectID string) ([]ProjectMember, error) {
	var owner ProjectMember
	var ownerName sql.NullString
	err := s.db.QueryRow(
		`SELECT u.id, u.email, u.name
		FROM projects p
		JOIN users u ON u.id = p.user_id
		WHERE p.id = ?`,
		projectID,
	).Scan(&owner.UserID, &owner.Email, &ownerName)
	if err != nil {
		return nil, err
	}
	if ownerName.Valid {
		owner.Name = ownerName.String
	}
	owner.Role = projectRoleOwner
	owner.IsOwner = true

	rows, err := s.db.Query(
		`SELECT u.id, u.email, u.name, pm.role
		FROM project_members pm
		JOIN users u ON u.id = pm.user_id
		JOIN projects p ON p.id = pm.project_id
		WHERE pm.project_id = ? AND pm.user_id <> p.user_id
		ORDER BY u.email ASC`,
		projectID,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	members := []ProjectMember{owner}
	for rows.Next() {
		var member ProjectMember
		var name sql.NullString
		if err := rows.Scan(&member.UserID, &member.Email, &name, &member.Role); err != nil {
			return nil, err
		}
		if name.Valid {
			member.Name = name.String
		}
		members = append(members, member)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}

	return members, nil
}

func redirectWithMessage(w http.ResponseWriter, r *http.Request, path string, statusMessage string, errorMessage string) {
	values := url.Values{}
	if statusMessage != "" {
		values.Set("status", statusMessage)
	}
	if errorMessage != "" {
		values.Set("error", errorMessage)
	}

	target := path
	if encoded := values.Encode(); encoded != "" {
		target = target + "?" + encoded
	}

	http.Redirect(w, r, target, http.StatusSeeOther)
}

func normalizeProjectPath(input string, allowEmpty bool) (string, error) {
	value := strings.TrimSpace(strings.ReplaceAll(input, "\\", "/"))
	if value == "" {
		if allowEmpty {
			return "", nil
		}
		return "", errors.New("path required")
	}
	if strings.HasPrefix(value, "/") {
		return "", errors.New("absolute paths are not allowed")
	}

	parts := strings.Split(value, "/")
	normalizedParts := make([]string, 0, len(parts))
	for _, part := range parts {
		if part == "" || part == "." {
			continue
		}
		if part == ".." {
			return "", errors.New("invalid path")
		}
		normalizedParts = append(normalizedParts, part)
	}

	normalized := path.Clean(strings.Join(normalizedParts, "/"))
	if normalized == "." {
		normalized = ""
	}
	if normalized == "" && !allowEmpty {
		return "", errors.New("path required")
	}
	return normalized, nil
}

func isPathWithinProject(projectDir, candidatePath string) bool {
	rel, err := filepath.Rel(projectDir, candidatePath)
	if err != nil {
		return false
	}
	if rel == "." {
		return true
	}
	if rel == ".." || strings.HasPrefix(rel, ".."+string(os.PathSeparator)) {
		return false
	}
	return true
}

func resolveProjectPath(projectDir, input string, allowEmpty bool) (string, string, error) {
	rel, err := normalizeProjectPath(input, allowEmpty)
	if err != nil {
		return "", "", err
	}

	abs := projectDir
	if rel != "" {
		abs = filepath.Join(projectDir, filepath.FromSlash(rel))
	}
	if !isPathWithinProject(projectDir, abs) {
		return "", "", errors.New("invalid path")
	}
	return rel, abs, nil
}

func hasHiddenSegment(rel string) bool {
	for _, segment := range strings.Split(rel, "/") {
		if segment != "" && strings.HasPrefix(segment, ".") {
			return true
		}
	}
	return false
}

func shouldSkipFileListEntry(rel string, isDir bool) bool {
	if hasHiddenSegment(rel) {
		return true
	}
	if isDir {
		return false
	}
	_, isArtifact := compilerArtifacts[rel]
	return isArtifact
}

func (s *Server) touchProject(projectID string) {
	s.db.Exec("UPDATE projects SET updated = datetime('now') WHERE id = ?", projectID)
}

func (s *Server) getProjectCompileEntry(projectID string) (string, error) {
	var entry string
	err := s.db.QueryRow("SELECT compile_entry FROM projects WHERE id = ?", projectID).Scan(&entry)
	return strings.TrimSpace(entry), err
}

func (s *Server) setProjectCompileEntry(projectID string, entry string) error {
	entry = strings.TrimSpace(entry)
	_, err := s.db.Exec(
		"UPDATE projects SET compile_entry = ?, updated = datetime('now') WHERE id = ?",
		entry, projectID,
	)
	return err
}

func (s *Server) preferredCompileEntry(projectID, projectDir string) (string, error) {
	storedEntry, err := s.getProjectCompileEntry(projectID)
	if err != nil && err != sql.ErrNoRows {
		return "", err
	}

	if storedEntry != "" {
		normalizedEntry, entryPath, resolveErr := resolveProjectPath(projectDir, storedEntry, false)
		if resolveErr == nil && isCompilableSource(normalizedEntry) {
			if entryInfo, statErr := os.Stat(entryPath); statErr == nil && !entryInfo.IsDir() {
				return normalizedEntry, nil
			}
		}
	}

	defaultEntry, err := defaultCompileEntry(projectDir)
	if err == errNoCompileEntry {
		return "", nil
	}
	if err != nil {
		return "", err
	}
	return defaultEntry, nil
}

func (s *Server) deleteCommentsForPath(projectID, path string) {
	pathPrefix := path + "/%"
	s.db.Exec(
		"DELETE FROM comments WHERE project_id = ? AND (file_path = ? OR file_path LIKE ?)",
		projectID, path, pathPrefix,
	)
}

func (s *Server) moveCommentsForPath(projectID, sourcePath, targetPath string) {
	pathPrefix := sourcePath + "/%"
	s.db.Exec(
		`UPDATE comments
		SET file_path = ? || substr(file_path, ?), updated = datetime('now')
		WHERE project_id = ? AND (file_path = ? OR file_path LIKE ?)`,
		targetPath, len(sourcePath)+1, projectID, sourcePath, pathPrefix,
	)
}

func (s *Server) handleListComments(w http.ResponseWriter, r *http.Request) {
	user := getUserFromContext(r.Context())
	access, ok := s.requireProjectAccess(w, r, projectAccessRead)
	if !ok {
		return
	}
	projectID := access.ProjectID

	queryPath := r.URL.Query().Get("file")
	filePath, err := normalizeProjectPath(queryPath, true)
	if err != nil {
		http.Error(w, "Invalid file path", http.StatusBadRequest)
		return
	}

	query := `
		SELECT id, project_id, file_path, start_line, end_line, body, snippet, author_id, author_email, created, updated
		FROM comments
		WHERE project_id = ?`
	args := []any{projectID}
	if filePath != "" {
		query += " AND file_path = ?"
		args = append(args, filePath)
	}
	query += " ORDER BY created ASC, id ASC"

	rows, err := s.db.Query(query, args...)
	if err != nil {
		http.Error(w, "Failed to load comments", http.StatusInternalServerError)
		return
	}
	defer rows.Close()

	comments := make([]Comment, 0)
	for rows.Next() {
		var comment Comment
		if err := rows.Scan(
			&comment.ID, &comment.ProjectID, &comment.FilePath, &comment.StartLine, &comment.EndLine,
			&comment.Body, &comment.Snippet, &comment.AuthorID, &comment.AuthorEmail, &comment.Created, &comment.Updated,
		); err != nil {
			http.Error(w, "Failed to load comments", http.StatusInternalServerError)
			return
		}
		comment.CanDelete = comment.AuthorID == user.ID
		comments = append(comments, comment)
	}
	if err := rows.Err(); err != nil {
		http.Error(w, "Failed to load comments", http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(comments)
}

func (s *Server) handleCreateComment(w http.ResponseWriter, r *http.Request) {
	user := getUserFromContext(r.Context())
	access, ok := s.requireProjectAccess(w, r, projectAccessRead)
	if !ok {
		return
	}
	if !canCommentOnProject(access) {
		http.Error(w, "Forbidden", http.StatusForbidden)
		return
	}
	projectID := access.ProjectID

	filePath, err := normalizeProjectPath(r.FormValue("filePath"), false)
	if err != nil {
		http.Error(w, "Invalid file path", http.StatusBadRequest)
		return
	}

	projectDir := filepath.Join(s.projectsDir, projectID)
	_, absFilePath, err := resolveProjectPath(projectDir, filePath, false)
	if err != nil {
		http.Error(w, "Invalid file path", http.StatusBadRequest)
		return
	}

	fileInfo, err := os.Stat(absFilePath)
	if os.IsNotExist(err) {
		http.Error(w, "File not found", http.StatusNotFound)
		return
	}
	if err != nil {
		http.Error(w, "Failed to access file", http.StatusInternalServerError)
		return
	}
	if fileInfo.IsDir() || !isTextFile(filePath) {
		http.Error(w, "Comments are only supported on text files", http.StatusBadRequest)
		return
	}

	startLine, err := parseLineValue(r.FormValue("startLine"))
	if err != nil {
		http.Error(w, "Invalid start line", http.StatusBadRequest)
		return
	}
	endLine, err := parseLineValue(r.FormValue("endLine"))
	if err != nil {
		http.Error(w, "Invalid end line", http.StatusBadRequest)
		return
	}
	if endLine < startLine {
		http.Error(w, "Invalid line range", http.StatusBadRequest)
		return
	}

	body := strings.TrimSpace(r.FormValue("body"))
	if body == "" {
		http.Error(w, "Comment body is required", http.StatusBadRequest)
		return
	}
	if len(body) > 2000 {
		http.Error(w, "Comment is too long", http.StatusBadRequest)
		return
	}

	snippet := strings.TrimSpace(r.FormValue("snippet"))
	if len(snippet) > 500 {
		snippet = snippet[:500]
	}

	commentID := uuid.New().String()
	_, err = s.db.Exec(
		`INSERT INTO comments
		(id, project_id, file_path, start_line, end_line, body, snippet, author_id, author_email)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		commentID, projectID, filePath, startLine, endLine, body, snippet, user.ID, user.Email,
	)
	if err != nil {
		http.Error(w, "Failed to create comment", http.StatusInternalServerError)
		return
	}

	comment := Comment{
		ID:          commentID,
		ProjectID:   projectID,
		FilePath:    filePath,
		StartLine:   startLine,
		EndLine:     endLine,
		Body:        body,
		Snippet:     snippet,
		AuthorID:    user.ID,
		AuthorEmail: user.Email,
		CanDelete:   true,
	}

	s.db.QueryRow("SELECT created, updated FROM comments WHERE id = ?", commentID).Scan(&comment.Created, &comment.Updated)
	s.touchProject(projectID)

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusCreated)
	json.NewEncoder(w).Encode(comment)
}

func (s *Server) handleDeleteComment(w http.ResponseWriter, r *http.Request) {
	user := getUserFromContext(r.Context())
	access, ok := s.requireProjectAccess(w, r, projectAccessRead)
	if !ok {
		return
	}
	projectID := access.ProjectID

	commentID := strings.TrimSpace(chi.URLParam(r, "commentID"))
	if commentID == "" {
		http.Error(w, "Comment not found", http.StatusNotFound)
		return
	}

	result, err := s.db.Exec(
		"DELETE FROM comments WHERE id = ? AND project_id = ? AND author_id = ?",
		commentID, projectID, user.ID,
	)
	if err != nil {
		http.Error(w, "Failed to delete comment", http.StatusInternalServerError)
		return
	}

	affected, _ := result.RowsAffected()
	if affected == 0 {
		http.Error(w, "Comment not found", http.StatusNotFound)
		return
	}

	s.touchProject(projectID)
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) handleListFiles(w http.ResponseWriter, r *http.Request) {
	access, ok := s.requireProjectAccess(w, r, projectAccessRead)
	if !ok {
		return
	}
	projectID := access.ProjectID

	projectDir := filepath.Join(s.projectsDir, projectID)
	var files []FileInfo
	err := filepath.WalkDir(projectDir, func(currentPath string, entry os.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if currentPath == projectDir {
			return nil
		}

		relPath, err := filepath.Rel(projectDir, currentPath)
		if err != nil {
			return err
		}
		relPath = filepath.ToSlash(relPath)
		if shouldSkipFileListEntry(relPath, entry.IsDir()) {
			if entry.IsDir() {
				return filepath.SkipDir
			}
			return nil
		}

		info, err := entry.Info()
		if err != nil {
			return err
		}

		contentType := ""
		isText := false
		if !entry.IsDir() {
			contentType = contentTypeForName(relPath)
			isText = isTextFile(relPath)
		}

		files = append(files, FileInfo{
			Name:        relPath,
			Path:        relPath,
			IsDir:       entry.IsDir(),
			Size:        info.Size(),
			IsText:      isText,
			ContentType: contentType,
		})

		return nil
	})
	if err != nil {
		http.Error(w, "Failed to list files", http.StatusInternalServerError)
		return
	}

	sort.Slice(files, func(i, j int) bool {
		if files[i].IsDir != files[j].IsDir {
			return files[i].IsDir
		}
		return files[i].Path < files[j].Path
	})

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(files)
}

func (s *Server) handleCreateFile(w http.ResponseWriter, r *http.Request) {
	access, ok := s.requireProjectAccess(w, r, projectAccessWrite)
	if !ok {
		return
	}
	projectID := access.ProjectID

	projectDir := filepath.Join(s.projectsDir, projectID)
	_, filePath, err := resolveProjectPath(projectDir, r.FormValue("filename"), false)
	if err != nil {
		http.Error(w, "Invalid filename", http.StatusBadRequest)
		return
	}

	if _, err := os.Stat(filePath); err == nil {
		http.Error(w, "File already exists", http.StatusConflict)
		return
	}

	entryType := strings.ToLower(strings.TrimSpace(r.FormValue("type")))
	if entryType == "dir" {
		if err := os.MkdirAll(filePath, 0755); err != nil {
			http.Error(w, "Failed to create folder", http.StatusInternalServerError)
			return
		}
	} else {
		if err := os.MkdirAll(filepath.Dir(filePath), 0755); err != nil {
			http.Error(w, "Failed to create file", http.StatusInternalServerError)
			return
		}
		if err := os.WriteFile(filePath, []byte(""), 0644); err != nil {
			http.Error(w, "Failed to create file", http.StatusInternalServerError)
			return
		}
	}

	s.touchProject(projectID)
	w.WriteHeader(http.StatusCreated)
}

func (s *Server) handleUploadFile(w http.ResponseWriter, r *http.Request) {
	access, ok := s.requireProjectAccess(w, r, projectAccessWrite)
	if !ok {
		return
	}
	projectID := access.ProjectID

	r.ParseMultipartForm(32 << 20)

	file, header, err := r.FormFile("file")
	if err != nil {
		http.Error(w, "Failed to read file", http.StatusBadRequest)
		return
	}
	defer file.Close()

	projectDir := filepath.Join(s.projectsDir, projectID)
	_, filePath, err := resolveProjectPath(projectDir, header.Filename, false)
	if err != nil {
		http.Error(w, "Invalid filename", http.StatusBadRequest)
		return
	}

	if err := os.MkdirAll(filepath.Dir(filePath), 0755); err != nil {
		http.Error(w, "Failed to create file path", http.StatusInternalServerError)
		return
	}

	dst, err := os.Create(filePath)
	if err != nil {
		http.Error(w, "Failed to create file", http.StatusInternalServerError)
		return
	}
	defer dst.Close()

	if _, err := io.Copy(dst, file); err != nil {
		http.Error(w, "Failed to save file", http.StatusInternalServerError)
		return
	}

	s.touchProject(projectID)
	w.WriteHeader(http.StatusCreated)
}

func (s *Server) handleGetFile(w http.ResponseWriter, r *http.Request) {
	access, ok := s.requireProjectAccess(w, r, projectAccessRead)
	if !ok {
		return
	}
	projectID := access.ProjectID

	projectDir := filepath.Join(s.projectsDir, projectID)
	filename, filePath, err := resolveProjectPath(projectDir, chi.URLParam(r, "*"), false)
	if err != nil {
		http.Error(w, "Invalid filename", http.StatusBadRequest)
		return
	}

	fileInfo, err := os.Stat(filePath)
	if err != nil {
		http.Error(w, "File not found", http.StatusNotFound)
		return
	}
	if fileInfo.IsDir() {
		http.Error(w, "Cannot read directory", http.StatusBadRequest)
		return
	}
	content, err := os.ReadFile(filePath)
	if err != nil {
		http.Error(w, "File not found", http.StatusNotFound)
		return
	}

	w.Header().Set("Content-Type", detectContentType(filename, content))
	w.Write(content)
}

func (s *Server) handleUpdateFile(w http.ResponseWriter, r *http.Request) {
	access, ok := s.requireProjectAccess(w, r, projectAccessWrite)
	if !ok {
		return
	}
	projectID := access.ProjectID

	projectDir := filepath.Join(s.projectsDir, projectID)
	_, filePath, err := resolveProjectPath(projectDir, chi.URLParam(r, "*"), false)
	if err != nil {
		http.Error(w, "Invalid filename", http.StatusBadRequest)
		return
	}

	fileInfo, err := os.Stat(filePath)
	if os.IsNotExist(err) {
		http.Error(w, "File not found", http.StatusNotFound)
		return
	}
	if err != nil {
		http.Error(w, "Failed to access file", http.StatusInternalServerError)
		return
	}
	if fileInfo.IsDir() {
		http.Error(w, "Cannot edit directory", http.StatusBadRequest)
		return
	}

	content := r.FormValue("content")
	if err := os.WriteFile(filePath, []byte(content), 0644); err != nil {
		http.Error(w, "Failed to save file", http.StatusInternalServerError)
		return
	}

	s.touchProject(projectID)
	w.WriteHeader(http.StatusOK)
}

func (s *Server) handleDeleteFile(w http.ResponseWriter, r *http.Request) {
	access, ok := s.requireProjectAccess(w, r, projectAccessWrite)
	if !ok {
		return
	}
	projectID := access.ProjectID

	projectDir := filepath.Join(s.projectsDir, projectID)
	filename, filePath, err := resolveProjectPath(projectDir, chi.URLParam(r, "*"), false)
	if err != nil {
		http.Error(w, "Invalid filename", http.StatusBadRequest)
		return
	}

	fileInfo, err := os.Stat(filePath)
	if os.IsNotExist(err) {
		http.Error(w, "File not found", http.StatusNotFound)
		return
	}
	if err != nil {
		http.Error(w, "Failed to access file", http.StatusInternalServerError)
		return
	}

	if fileInfo.IsDir() {
		err = os.RemoveAll(filePath)
	} else {
		err = os.Remove(filePath)
	}
	if err != nil {
		http.Error(w, "Failed to delete file", http.StatusInternalServerError)
		return
	}

	s.deleteCommentsForPath(projectID, filename)
	s.touchProject(projectID)
	w.WriteHeader(http.StatusOK)
}

func (s *Server) handleMoveFile(w http.ResponseWriter, r *http.Request) {
	access, ok := s.requireProjectAccess(w, r, projectAccessWrite)
	if !ok {
		return
	}
	projectID := access.ProjectID

	projectDir := filepath.Join(s.projectsDir, projectID)

	sourceRel, sourcePath, err := resolveProjectPath(projectDir, r.FormValue("source"), false)
	if err != nil {
		http.Error(w, "Invalid source path", http.StatusBadRequest)
		return
	}
	sourceInfo, err := os.Stat(sourcePath)
	if os.IsNotExist(err) {
		http.Error(w, "Source not found", http.StatusNotFound)
		return
	}
	if err != nil {
		http.Error(w, "Failed to access source path", http.StatusInternalServerError)
		return
	}

	var targetRel string
	if target := r.FormValue("target"); target != "" {
		var targetPath string
		targetRel, targetPath, err = resolveProjectPath(projectDir, target, false)
		if err != nil {
			http.Error(w, "Invalid target path", http.StatusBadRequest)
			return
		}
		targetParent := filepath.Dir(targetPath)
		if targetParent != projectDir {
			if info, statErr := os.Stat(targetParent); statErr != nil || !info.IsDir() {
				http.Error(w, "Target parent directory not found", http.StatusBadRequest)
				return
			}
		}
		if sourceInfo.IsDir() && strings.HasPrefix(targetRel+"/", sourceRel+"/") {
			http.Error(w, "Cannot move a directory into itself", http.StatusBadRequest)
			return
		}
	} else {
		targetDirRel, targetDirPath, err := resolveProjectPath(projectDir, r.FormValue("targetDir"), true)
		if err != nil {
			http.Error(w, "Invalid target path", http.StatusBadRequest)
			return
		}
		if targetDirRel != "" {
			targetInfo, err := os.Stat(targetDirPath)
			if os.IsNotExist(err) {
				http.Error(w, "Target directory not found", http.StatusBadRequest)
				return
			}
			if err != nil {
				http.Error(w, "Failed to access target path", http.StatusInternalServerError)
				return
			}
			if !targetInfo.IsDir() {
				http.Error(w, "Target must be a directory", http.StatusBadRequest)
				return
			}
		}

		if sourceInfo.IsDir() && (targetDirRel == sourceRel || strings.HasPrefix(targetDirRel+"/", sourceRel+"/")) {
			http.Error(w, "Cannot move a directory into itself", http.StatusBadRequest)
			return
		}

		targetRel = path.Join(targetDirRel, path.Base(sourceRel))
	}
	if targetRel == sourceRel {
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]string{"path": targetRel})
		return
	}

	targetPath := filepath.Join(projectDir, filepath.FromSlash(targetRel))
	if !isPathWithinProject(projectDir, targetPath) {
		http.Error(w, "Invalid target path", http.StatusBadRequest)
		return
	}

	if _, err := os.Stat(targetPath); err == nil {
		http.Error(w, "Target already exists", http.StatusConflict)
		return
	} else if !os.IsNotExist(err) {
		http.Error(w, "Failed to access target path", http.StatusInternalServerError)
		return
	}

	if err := os.Rename(sourcePath, targetPath); err != nil {
		http.Error(w, "Failed to move file", http.StatusInternalServerError)
		return
	}

	s.moveCommentsForPath(projectID, sourceRel, targetRel)
	s.touchProject(projectID)
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]string{"path": targetRel})
}
