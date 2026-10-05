package app

import (
	"context"
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
printf '%s\n' "$@" > tectonic.args
# Mirror tectonic --outdir: write the PDF into the build directory.
mkdir -p .polytex-build
printf 'fake-tex-pdf' > .polytex-build/main.pdf
`)
	writeExecutable(t, typstPath, `#!/bin/sh
set -eu
printf '%s\n' "$@" > typst.args
exit 1
`)

	compiler := NewCompiler(tectonicPath, typstPath)
	pdf, output, err := compiler.Compile(context.Background(), workDir, "main.tex")
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
	want := []string{"--outdir", ".polytex-build", "--keep-logs", "main.tex"}
	if len(args) != len(want) {
		t.Fatalf("unexpected tectonic args %v", args)
	}
	for i := range want {
		if args[i] != want[i] {
			t.Fatalf("tectonic arg %d=%q want %q", i, args[i], want[i])
		}
	}

	if _, err := os.ReadFile(filepath.Join(workDir, filepath.FromSlash(CompiledPDFRelPath))); err != nil {
		t.Fatalf("expected compiled PDF in the build dir: %v", err)
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
	pdf, output, err := compiler.Compile(context.Background(), workDir, "book/main.typ")
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
	want := []string{"compile", "book/main.typ", ".polytex-build/main.pdf"}
	if len(args) != len(want) {
		t.Fatalf("unexpected typst args %v", args)
	}
	for i := range want {
		if args[i] != want[i] {
			t.Fatalf("typst arg %d=%q want %q", i, args[i], want[i])
		}
	}

	mainPDF, err := os.ReadFile(filepath.Join(workDir, filepath.FromSlash(CompiledPDFRelPath)))
	if err != nil {
		t.Fatalf("expected compiled PDF in the build dir: %v", err)
	}
	if string(mainPDF) != "fake-typst-pdf" {
		t.Fatalf("unexpected compiled PDF payload %q", string(mainPDF))
	}
}

func TestCompilerCompileUnsupportedExtension(t *testing.T) {
	t.Parallel()

	compiler := NewCompiler("tectonic", "typst")
	_, _, err := compiler.Compile(context.Background(), t.TempDir(), "notes.md")
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
