package app

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestCompileUsesMinimalEnvironment verifies the compiler never hands the
// server's own environment (which holds JWT_SECRET) to a compile, even when the
// sandbox itself is disabled, as it is on non-Linux CI.
func TestCompileUsesMinimalEnvironment(t *testing.T) {
	workDir := t.TempDir()
	tectonicPath := filepath.Join(workDir, "tectonic-fake")
	envDump := filepath.Join(workDir, "env.txt")

	writeExecutable(t, tectonicPath, `#!/bin/sh
env > `+envDump+`
mkdir -p .polytex-build
printf 'pdf' > .polytex-build/main.pdf
`)

	t.Setenv("JWT_SECRET", "top-secret-value")
	t.Setenv("PATH", os.Getenv("PATH"))

	compiler := NewCompiler(tectonicPath, filepath.Join(workDir, "typst-fake"))
	if _, output, err := compiler.Compile(context.Background(), workDir, "main.tex"); err != nil {
		t.Fatalf("Compile returned error: %v (output=%q)", err, output)
	}

	dumped, err := os.ReadFile(envDump)
	if err != nil {
		t.Fatalf("compiler did not run: %v", err)
	}
	if strings.Contains(string(dumped), "top-secret-value") {
		t.Fatalf("JWT_SECRET leaked into the compile environment:\n%s", dumped)
	}
}
