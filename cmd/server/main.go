package main

import (
	"crypto/rand"
	"encoding/hex"
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
	jwtSecret := resolveJWTSecret()
	dataDir := getEnv("DATA_DIR", "data")
	secureCookies := isTruthy(os.Getenv("SECURE_COOKIES"))
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

	server, err := app.NewServer(database, jwtSecret, projectsDir, tectonicBin, typstBin, gitBin, secureCookies)
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

// resolveJWTSecret returns the signing and encryption secret. An unset secret
// yields a random ephemeral one so development just works, at the cost of
// sessions and stored SSH keys not surviving a restart. The shipped placeholder
// is refused outright, so it can never reach production by being copied from an
// example.
func resolveJWTSecret() string {
	secret := os.Getenv("JWT_SECRET")
	switch {
	case secret == "":
		buf := make([]byte, 32)
		if _, err := rand.Read(buf); err != nil {
			log.Fatal("Failed to generate an ephemeral JWT secret:", err)
		}
		log.Println("WARNING: JWT_SECRET is not set. Using a random ephemeral secret; " +
			"sessions and stored SSH keys will not survive a restart. Set JWT_SECRET for production.")
		return hex.EncodeToString(buf)
	case secret == "change-me-in-production":
		log.Fatal("JWT_SECRET is set to the example placeholder. Set a real secret, e.g. openssl rand -hex 32.")
	case len(secret) < 16:
		log.Println("WARNING: JWT_SECRET is shorter than 16 characters. Use a longer random value, e.g. openssl rand -hex 32.")
	}
	return secret
}

func isTruthy(value string) bool {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "1", "true", "yes", "on":
		return true
	}
	return false
}

func getEnv(key, fallback string) string {
	if value := os.Getenv(key); value != "" {
		return value
	}
	return fallback
}
