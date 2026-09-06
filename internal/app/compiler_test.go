package app

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestCompilerCompileTeX(t *testing.T) {
	t.Parallel()

	workDir := t.TempDir()
	tectonicPath := filepath.Join(workDir, "tectonic-fake")
	typstPath := filepath.Join(workDir, "typst-fake")

	writeExecutable(t, tectonicPath, `#!/bin/sh
set -eu
entry="$1"
pdf="${entry%.*}.pdf"
printf '%s\n' "$@" > tectonic.args
printf 'fake-tex-pdf' > "$pdf"
`)
	writeExecutable(t, typstPath, `#!/bin/sh
set -eu
printf '%s\n' "$@" > typst.args
exit 1
`)

	compiler := NewCompiler(tectonicPath, typstPath)
	pdf, output, err := compiler.Compile(workDir, "main.tex")
	if err != nil {
		t.Fatalf("Compile returned error: %v (output=%q)", err, output)
	}
	if string(pdf) != "fake-tex-pdf" {
		t.Fatalf("unexpected pdf payload %q", string(pdf))
	}

	argsContent, err := os.ReadFile(filepath.Join(workDir, "tectonic.args"))
	if err != nil {
		t.Fatalf("failed reading tectonic args: %v", err)
	}
	args := strings.Fields(string(argsContent))
	want := []string{"main.tex"}
	if len(args) != len(want) {
		t.Fatalf("unexpected tectonic args %v", args)
	}
	for i := range want {
		if args[i] != want[i] {
			t.Fatalf("tectonic arg %d=%q want %q", i, args[i], want[i])
		}
	}
}

func TestCompilerCompileTypst(t *testing.T) {
	t.Parallel()

	workDir := t.TempDir()
	tectonicPath := filepath.Join(workDir, "tectonic-fake")
	typstPath := filepath.Join(workDir, "typst-fake")

	writeExecutable(t, tectonicPath, `#!/bin/sh
set -eu
printf '%s\n' "$@" > tectonic.args
exit 1
`)
	writeExecutable(t, typstPath, `#!/bin/sh
set -eu
output="$3"
printf '%s\n' "$@" > typst.args
mkdir -p "$(dirname "$output")"
printf 'fake-typst-pdf' > "$output"
`)

	compiler := NewCompiler(tectonicPath, typstPath)
	pdf, output, err := compiler.Compile(workDir, "book/main.typ")
	if err != nil {
		t.Fatalf("Compile returned error: %v (output=%q)", err, output)
	}
	if string(pdf) != "fake-typst-pdf" {
		t.Fatalf("unexpected pdf payload %q", string(pdf))
	}

	argsContent, err := os.ReadFile(filepath.Join(workDir, "typst.args"))
	if err != nil {
		t.Fatalf("failed reading typst args: %v", err)
	}
	args := strings.Fields(string(argsContent))
	want := []string{"compile", "book/main.typ", "book/main.pdf"}
	if len(args) != len(want) {
		t.Fatalf("unexpected typst args %v", args)
	}
	for i := range want {
		if args[i] != want[i] {
			t.Fatalf("typst arg %d=%q want %q", i, args[i], want[i])
		}
	}

	mainPDF, err := os.ReadFile(filepath.Join(workDir, "main.pdf"))
	if err != nil {
		t.Fatalf("expected canonical main.pdf copy: %v", err)
	}
	if string(mainPDF) != "fake-typst-pdf" {
		t.Fatalf("unexpected main.pdf payload %q", string(mainPDF))
	}
}

func TestCompilerCompileUnsupportedExtension(t *testing.T) {
	t.Parallel()

	compiler := NewCompiler("tectonic", "typst")
	_, _, err := compiler.Compile(t.TempDir(), "notes.md")
	if err == nil {
		t.Fatalf("expected error for unsupported extension")
	}
}

func writeExecutable(t *testing.T, path string, content string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(content), 0755); err != nil {
		t.Fatalf("failed to write executable %s: %v", path, err)
	}
}
