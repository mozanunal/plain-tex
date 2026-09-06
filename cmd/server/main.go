package main

import (
	"log"
	"net/http"
	"os"
	"path/filepath"

	"github.com/mozanunal/poly-txt/internal/app"
	"github.com/mozanunal/poly-txt/internal/db"
)

func main() {
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

	log.Printf("Starting server on http://localhost:%s", port)
	if err := http.ListenAndServe(":"+port, server); err != nil {
		log.Fatal("Server failed:", err)
	}
}

func getEnv(key, fallback string) string {
	if value := os.Getenv(key); value != "" {
		return value
	}
	return fallback
}
