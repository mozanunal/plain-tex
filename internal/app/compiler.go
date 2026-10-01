package app

import (
	"context"
	"crypto/md5"
	"encoding/hex"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/mozanunal/poly-txt/internal/procenv"
	"github.com/mozanunal/poly-txt/internal/sandbox"
)

// SandboxConfig controls how compiles are confined. When Enabled, each compile
// runs through the sandbox helper with read-write access to the project
// directory only and read-only access to CacheDirs (the Tectonic and Typst
// caches, fonts, and the toolchain). The limits bound a runaway or malicious
// document.
type SandboxConfig struct {
	Enabled       bool
	SelfPath      string
	CacheDirs     []string
	CPUSeconds    uint64
	MemoryBytes   uint64
	FileSizeBytes uint64
	MaxProcesses  uint64
}

type Compiler struct {
	tectonicBin string
	typstBin    string
	sandbox     SandboxConfig
}

func NewCompiler(tectonicBin string, typstBin string) *Compiler {
	if tectonicBin == "" {
		tectonicBin = "tectonic"
	}
	if typstBin == "" {
		typstBin = "typst"
	}
	return &Compiler{
		tectonicBin: tectonicBin,
		typstBin:    typstBin,
	}
}

// SetSandbox installs the confinement used for every subsequent compile.
func (c *Compiler) SetSandbox(cfg SandboxConfig) {
	c.sandbox = cfg
}

// command builds the exec.Cmd for one compile, wrapped in the sandbox helper
// when enabled. The child never inherits the server environment, so JWT_SECRET
// is not reachable through /proc/self/environ even when the sandbox is off.
func (c *Compiler) command(ctx context.Context, workDir, name string, args ...string) (*exec.Cmd, error) {
	readOnly := make([]string, 0, len(c.sandbox.CacheDirs)+2)
	readOnly = append(readOnly, c.sandbox.CacheDirs...)
	for _, dir := range []string{"/usr", "/etc", "/bin", "/lib", "/lib64", "/opt", homeDir()} {
		if dir != "" {
			readOnly = append(readOnly, dir)
		}
	}

	env := procenv.Minimal()

	if c.sandbox.Enabled {
		spec := sandbox.Spec{
			ReadWrite:     []string{workDir},
			ReadOnly:      readOnly,
			CPUSeconds:    c.sandbox.CPUSeconds,
			MemoryBytes:   c.sandbox.MemoryBytes,
			FileSizeBytes: c.sandbox.FileSizeBytes,
			MaxProcesses:  c.sandbox.MaxProcesses,
		}
		cmd, err := sandbox.Command(ctx, c.sandbox.SelfPath, spec, env, name, args...)
		if err != nil {
			return nil, err
		}
		cmd.Dir = workDir
		configureProcessGroup(cmd)
		return cmd, nil
	}

	cmd := exec.CommandContext(ctx, name, args...)
	cmd.Dir = workDir
	cmd.Env = env
	configureProcessGroup(cmd)
	return cmd, nil
}

func homeDir() string {
	if home, err := os.UserHomeDir(); err == nil {
		return home
	}
	return ""
}

func (c *Compiler) Compile(ctx context.Context, workDir string, entryFile string) ([]byte, string, error) {
	if err := os.MkdirAll(workDir, 0755); err != nil {
		return nil, "", err
	}

	entryFile = filepath.ToSlash(strings.TrimSpace(entryFile))
	if entryFile == "" {
		return nil, "", fmt.Errorf("entry file is required")
	}

	ext := strings.ToLower(filepath.Ext(entryFile))

	// Compiled output and intermediates go to a hidden build directory, never
	// next to the source. That keeps the project tree clean, and it means a
	// figure a user commits as fig.pdf is no longer mistaken for build output
	// and excluded from Git.
	if err := os.MkdirAll(filepath.Join(workDir, buildDirName), 0755); err != nil {
		return nil, "", err
	}
	pdfFile := buildDirName + "/" + "main.pdf"

	var name string
	var args []string
	var wrapperFile string

	switch ext {
	case ".tex":
		// The V1 CLI is used instead of "-X compile" because the V2 CLI is only
		// available in tectonic builds compiled with the "serialization" feature,
		// which most distribution packages omit. Both produce identical output.
		name, args = c.tectonicBin, []string{"--outdir", buildDirName, "--keep-logs", entryFile}
		pdfFile = buildDirName + "/" + pdfBaseName(entryFile)
	case ".typ":
		name, args = c.typstBin, []string{"compile", entryFile, pdfFile}
	case ".md":
		hash := md5.Sum([]byte(entryFile))
		wrapperName := ".md-wrapper-" + hex.EncodeToString(hash[:8]) + ".typ"
		wrapperFile = filepath.Join(workDir, wrapperName)

		wrapperContent := fmt.Sprintf(`#import "@preview/cmarker:0.1.8"
#cmarker.render(read(%s))
`, typstStringLiteral(entryFile))

		if err := os.WriteFile(wrapperFile, []byte(wrapperContent), 0644); err != nil {
			return nil, "", fmt.Errorf("failed to create markdown wrapper: %w", err)
		}

		name, args = c.typstBin, []string{"compile", wrapperName, pdfFile}
	default:
		return nil, "", fmt.Errorf("unsupported entry file: %s", entryFile)
	}

	cmd, err := c.command(ctx, workDir, name, args...)
	if err != nil {
		return nil, "", err
	}
	output, err := cmd.CombinedOutput()

	if wrapperFile != "" {
		_ = os.Remove(wrapperFile)
	}

	if err != nil {
		if len(output) == 0 {
			output = []byte(err.Error())
		}
		return nil, string(output), err
	}

	pdfPath := filepath.Join(workDir, filepath.FromSlash(pdfFile))
	pdf, readErr := os.ReadFile(pdfPath)
	if readErr != nil {
		return nil, string(output), readErr
	}

	if filepath.ToSlash(pdfFile) != CompiledPDFRelPath {
		_ = os.WriteFile(filepath.Join(workDir, filepath.FromSlash(CompiledPDFRelPath)), pdf, 0644)
	}

	return pdf, string(output), nil
}

// buildDirName is the hidden per-project directory that holds compiled output
// and intermediates. It starts with a dot, so the file browser and Git exclude
// it the same way they exclude other dotfiles.
const buildDirName = ".polytex-build"

// CompiledPDFRelPath is where the latest compiled PDF is always written,
// relative to the project directory.
const CompiledPDFRelPath = buildDirName + "/main.pdf"

func pdfBaseName(entryFile string) string {
	base := filepath.Base(entryFile)
	return strings.TrimSuffix(base, filepath.Ext(base)) + ".pdf"
}

// typstStringLiteral renders s as a Typst double-quoted string, escaping the two
// characters that are special inside one, so a crafted filename cannot break out
// of the read() call in the Markdown wrapper.
func typstStringLiteral(s string) string {
	replacer := strings.NewReplacer(`\`, `\\`, `"`, `\"`)
	return `"` + replacer.Replace(s) + `"`
}

// Default compile limits used when the sandbox is enabled. They are generous
// enough for a large thesis but stop a runaway or malicious document from
// exhausting the host.
const (
	DefaultCompileCPUSeconds    = 120
	DefaultCompileMemoryBytes   = 2 << 30   // 2 GiB
	DefaultCompileFileSizeBytes = 512 << 20 // 512 MiB per output file
	DefaultCompileMaxProcesses  = 64
)
