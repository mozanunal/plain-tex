package app

import (
	"database/sql"
	"html/template"
	"net/http"

	"github.com/mozanunal/poly-txt/internal/gitclient"
)

type Server struct {
	db          *sql.DB
	compiler    *Compiler
	git         *gitclient.Client
	templates   *template.Template
	jwtSecret   []byte
	projectsDir string
	router      http.Handler
}

func NewServer(db *sql.DB, jwtSecret string, projectsDir string, tectonicBin string, typstBin string, gitBin string) (*Server, error) {
	s := &Server{
		db:          db,
		compiler:    NewCompiler(tectonicBin, typstBin),
		git:         gitclient.New(gitBin),
		jwtSecret:   []byte(jwtSecret),
		projectsDir: projectsDir,
	}

	tmpl, err := loadTemplates()
	if err != nil {
		return nil, err
	}
	s.templates = tmpl

	s.router = s.setupRoutes()

	return s, nil
}

func (s *Server) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	s.router.ServeHTTP(w, r)
}
