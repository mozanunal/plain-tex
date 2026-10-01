package main

import (
	"log"
	"net/http"
	"os"
	"path/filepath"
	"strings"

	"github.com/mozanunal/poly-txt/internal/app"
	"github.com/mozanunal/poly-txt/internal/db"
	"github.com/mozanunal/poly-txt/internal/sandbox"
)

func main() {
	// A sandbox helper re-execs this binary, applies its restrictions, then
	// becomes the compiler. Handle that before anything else so the helper never
	// opens the database or starts the server.
	if sandbox.IsHelperInvocation(os.Args) {
		os.Exit(sandbox.RunHelper(os.Args))
	}

	port := getEnv("PORT", "3000")
	jwtSecret := getEnv("JWT_SECRET", "change-me-in-production")
	dataDir := getEnv("DATA_DIR", "data")
	tectonicBin := getEnv("TECTONIC_BIN", "tectonic")
	typstBin := getEnv("TYPST_BIN", "typst")
	gitBin := getEnv("GIT_BIN", "git")

	dbPath := filepath.Join(dataDir, "latex.db")
	projectsDir := filepath.Join(dataDir, "projects")

	if err := os.MkdirAll(projectsDir, 0755); err != nil {
		log.Fatal("Failed to create projects directory:", err)
	}

	database, err := db.Open(dbPath)
	if err != nil {
		log.Fatal("Failed to open database:", err)
	}
	defer database.Close()

	server, err := app.NewServer(database, jwtSecret, projectsDir, tectonicBin, typstBin, gitBin)
	if err != nil {
		log.Fatal("Failed to create server:", err)
	}

	configureCompileSandbox(server, dataDir)

	log.Printf("Starting server on http://localhost:%s", port)
	if err := http.ListenAndServe(":"+port, server); err != nil {
		log.Fatal("Server failed:", err)
	}
}

// configureCompileSandbox turns on compile confinement according to the SANDBOX
// setting: "required" refuses to start without it, "off" disables it, and the
// default enables it wherever the platform supports it.
func configureCompileSandbox(server *app.Server, dataDir string) {
	mode := strings.ToLower(strings.TrimSpace(getEnv("SANDBOX", "auto")))
	available := sandbox.Available()

	if mode == "required" && !available {
		log.Fatal("SANDBOX=required but the compile sandbox is unavailable (needs Linux with Landlock). " +
			"Set SANDBOX=off to run without it, accepting that a document could read or write any file the server can.")
	}

	enabled := available && mode != "off"
	if !enabled {
		log.Printf("Compile sandbox: DISABLED (mode=%s, available=%t). Compiles run unconfined.", mode, available)
		server.ConfigureCompileSandbox(app.SandboxConfig{Enabled: false})
		return
	}

	self, err := os.Executable()
	if err != nil {
		log.Fatal("Failed to locate the server binary for the compile sandbox:", err)
	}

	cacheDirs := compileCacheDirs(dataDir)
	server.ConfigureCompileSandbox(app.SandboxConfig{
		Enabled:       true,
		SelfPath:      self,
		CacheDirs:     cacheDirs,
		CPUSeconds:    app.DefaultCompileCPUSeconds,
		MemoryBytes:   app.DefaultCompileMemoryBytes,
		FileSizeBytes: app.DefaultCompileFileSizeBytes,
		MaxProcesses:  app.DefaultCompileMaxProcesses,
	})
	log.Printf("Compile sandbox: ENABLED (read-write: a project dir only; read-only: %s).", strings.Join(cacheDirs, ", "))
}

func compileCacheDirs(dataDir string) []string {
	dirs := []string{filepath.Join(dataDir, "cache")}
	for _, key := range []string{"TECTONIC_CACHE_DIR", "XDG_CACHE_HOME", "TYPST_PACKAGE_CACHE_PATH"} {
		if value := strings.TrimSpace(os.Getenv(key)); value != "" {
			dirs = append(dirs, value)
		}
	}
	return dirs
}

func getEnv(key, fallback string) string {
	if value := os.Getenv(key); value != "" {
		return value
	}
	return fallback
}
