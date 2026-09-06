package app

import (
	"io/fs"
	"net/http"

	"github.com/go-chi/chi/v5"
	"github.com/go-chi/chi/v5/middleware"
)

const healthPath = "/healthz"

// requestLogger logs every request except the health endpoint, which a container
// or uptime monitor polls often enough to drown out the rest of the log.
func requestLogger(next http.Handler) http.Handler {
	logged := middleware.Logger(next)

	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == healthPath {
			next.ServeHTTP(w, r)
			return
		}
		logged.ServeHTTP(w, r)
	})
}

func (s *Server) setupRoutes() http.Handler {
	r := chi.NewRouter()

	r.Use(requestLogger)
	r.Use(middleware.Recoverer)

	staticContent, _ := fs.Sub(staticFS, "static")
	r.Handle("/static/*", http.StripPrefix("/static/", http.FileServer(http.FS(staticContent))))

	r.Get(healthPath, s.handleHealth)

	r.Get("/login", s.handleLoginPage)
	r.Post("/login", s.handleLogin)
	r.Get("/register", s.handleRegisterPage)
	r.Post("/register", s.handleRegister)
	r.Post("/logout", s.handleLogout)

	r.Group(func(r chi.Router) {
		r.Use(s.authMiddleware)

		r.Get("/", s.handleProjectsPage)
		r.Get("/settings", s.handleSettingsPage)
		r.Post("/settings/profile", s.handleUpdateProfile)
		r.Post("/settings/password", s.handleChangePassword)
		r.Get("/admin/users", s.handleAdminUsersPage)
		r.Post("/projects", s.handleCreateProject)
		r.Post("/projects/clone", s.handleCloneProject)
		r.Post("/user/git-key/generate", s.handleGenerateUserGitKey)
		r.Post("/user/git-key/delete", s.handleDeleteUserGitKey)
		r.Delete("/projects/{id}", s.handleDeleteProject)
		r.Post("/projects/{id}/members", s.handleAddProjectMember)
		r.Post("/projects/{id}/members/{userID}/remove", s.handleRemoveProjectMember)
		r.Post("/admin/users", s.handleAdminCreateUser)
		r.Post("/admin/users/{userID}/reset-password", s.handleAdminResetPassword)

		r.Get("/editor/{id}", s.handleEditorPage)
		r.Post("/compile/{id}", s.handleCompile)
		r.Post("/save/{id}", s.handleSave)
		r.Get("/pdf/{id}", s.handleGetPDF)
		r.Get("/api/projects/{id}/download/source", s.handleDownloadSource)
		r.Get("/api/projects/{id}/download/pdf", s.handleDownloadPDF)
		r.Post("/api/projects/{id}/compile-target", s.handleUpdateCompileTarget)
		r.Get("/api/projects/{id}/git/status", s.handleGitStatus)
		r.Post("/api/projects/{id}/git/config", s.handleGitConfig)
		r.Post("/api/projects/{id}/git/pull", s.handleGitPull)
		r.Post("/api/projects/{id}/git/pull-rebase", s.handleGitPullRebase)
		r.Post("/api/projects/{id}/git/reset-to-remote", s.handleGitResetToRemote)
		r.Post("/api/projects/{id}/git/push", s.handleGitPush)
		r.Post("/api/projects/{id}/git/reset", s.handleGitReset)

		r.Get("/api/projects/{id}/files", s.handleListFiles)
		r.Post("/api/projects/{id}/files", s.handleCreateFile)
		r.Post("/api/projects/{id}/files/move", s.handleMoveFile)
		r.Post("/api/projects/{id}/upload", s.handleUploadFile)
		r.Get("/api/projects/{id}/files/*", s.handleGetFile)
		r.Put("/api/projects/{id}/files/*", s.handleUpdateFile)
		r.Delete("/api/projects/{id}/files/*", s.handleDeleteFile)
		r.Get("/api/projects/{id}/comments", s.handleListComments)
		r.Post("/api/projects/{id}/comments", s.handleCreateComment)
		r.Delete("/api/projects/{id}/comments/{commentID}", s.handleDeleteComment)
	})

	return r
}
