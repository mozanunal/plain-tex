package app

import (
	"crypto/md5"
	"encoding/hex"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

type Compiler struct {
	tectonicBin string
	typstBin    string
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

func (c *Compiler) Compile(workDir string, entryFile string) ([]byte, string, error) {
	if err := os.MkdirAll(workDir, 0755); err != nil {
		return nil, "", err
	}

	entryFile = filepath.ToSlash(strings.TrimSpace(entryFile))
	if entryFile == "" {
		return nil, "", fmt.Errorf("entry file is required")
	}

	ext := strings.ToLower(filepath.Ext(entryFile))
	pdfFile := strings.TrimSuffix(entryFile, filepath.Ext(entryFile)) + ".pdf"

	var cmd *exec.Cmd
	var wrapperFile string

	switch ext {
	case ".tex":
		// The V1 CLI is used instead of "-X compile" because the V2 CLI is only
		// available in tectonic builds compiled with the "serialization" feature,
		// which most distribution packages omit. Both produce identical output.
		cmd = exec.Command(c.tectonicBin, entryFile)
	case ".typ":
		cmd = exec.Command(c.typstBin, "compile", entryFile, pdfFile)
	case ".md":
		hash := md5.Sum([]byte(entryFile))
		wrapperName := ".md-wrapper-" + hex.EncodeToString(hash[:8]) + ".typ"
		wrapperFile = filepath.Join(workDir, wrapperName)

		wrapperContent := fmt.Sprintf(`#import "@preview/cmarker:0.1.8"
#cmarker.render(read("%s"))
`, entryFile)

		if err := os.WriteFile(wrapperFile, []byte(wrapperContent), 0644); err != nil {
			return nil, "", fmt.Errorf("failed to create markdown wrapper: %w", err)
		}

		pdfFile = strings.TrimSuffix(entryFile, filepath.Ext(entryFile)) + ".pdf"
		cmd = exec.Command(c.typstBin, "compile", wrapperName, pdfFile)
	default:
		return nil, "", fmt.Errorf("unsupported entry file: %s", entryFile)
	}

	cmd.Dir = workDir
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

	if filepath.ToSlash(pdfFile) != "main.pdf" {
		_ = os.WriteFile(filepath.Join(workDir, "main.pdf"), pdf, 0644)
	}

	return pdf, string(output), nil
}
