package app

import (
	"database/sql"
	"html/template"
	"io/fs"
	"log"
	"net/http"
	"runtime"
	"sync"

	"github.com/mozanunal/poly-txt/internal/gitclient"
)

type Server struct {
	db            *sql.DB
	compiler      *Compiler
	git           *gitclient.Client
	templates     *template.Template
	jwtSecret     []byte
	projectsDir   string
	secureCookies bool
	router        http.Handler

	// compileSem caps how many compiles run at once; projectLocks serializes
	// work that touches one project's files (a compile and a git operation must
	// not run against the same working tree at the same time).
	compileSem   chan struct{}
	projectLocks *keyedMutex
}

func NewServer(db *sql.DB, jwtSecret string, projectsDir string, tectonicBin string, typstBin string, gitBin string, secureCookies bool) (*Server, error) {
	s := &Server{
		db:            db,
		compiler:      NewCompiler(tectonicBin, typstBin),
		git:           gitclient.New(gitBin),
		jwtSecret:     []byte(jwtSecret),
		projectsDir:   projectsDir,
		secureCookies: secureCookies,
		compileSem:    make(chan struct{}, maxConcurrentCompiles()),
		projectLocks:  newKeyedMutex(),
	}

	tmpl, err := loadTemplates()
	if err != nil {
		return nil, err
	}
	s.templates = tmpl

	if _, err := fs.Stat(staticFS, "static/vendor"); err != nil {
		log.Print("WARNING: Monaco and pdf.js are missing from this build, so the editor will not load. " +
			"Run `make vendor` (or build with `make build`) before compiling the binary.")
	}

	s.router = s.setupRoutes()

	return s, nil
}

// ConfigureCompileSandbox installs the confinement used for every compile.
func (s *Server) ConfigureCompileSandbox(cfg SandboxConfig) {
	s.compiler.SetSandbox(cfg)
}

func (s *Server) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	s.router.ServeHTTP(w, r)
}

func maxConcurrentCompiles() int {
	if n := runtime.NumCPU(); n > 1 {
		return n
	}
	return 1
}

// keyedMutex hands out one mutex per key, so callers can serialize work on a
// single project without serializing unrelated projects.
type keyedMutex struct {
	mu    sync.Mutex
	locks map[string]*sync.Mutex
}

func newKeyedMutex() *keyedMutex {
	return &keyedMutex{locks: make(map[string]*sync.Mutex)}
}

func (k *keyedMutex) Lock(key string) func() {
	k.mu.Lock()
	lock, ok := k.locks[key]
	if !ok {
		lock = &sync.Mutex{}
		k.locks[key] = lock
	}
	k.mu.Unlock()

	lock.Lock()
	return lock.Unlock
}
