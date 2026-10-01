// Package sandbox confines a child process to a set of directories and resource
// limits. It is used to run the LaTeX and Typst compilers, which execute
// user-controlled documents that can otherwise read and write any file the
// server can (see the compile sandbox notes). Confinement is enforced by a
// short-lived helper that re-execs the server binary: the helper restricts
// itself, then becomes the real compiler, so the restrictions are in force for
// the whole compile but never touch the server process.
package sandbox

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"

	"github.com/mozanunal/poly-txt/internal/procenv"
)

// helperCommand is the first argument that marks a re-exec of this binary as the
// sandbox helper rather than the server.
const helperCommand = "__sandbox_exec"

// Spec describes the confinement for one child process.
type Spec struct {
	// ReadWrite and ReadOnly are absolute directories the child may access.
	ReadWrite []string `json:"rw"`
	ReadOnly  []string `json:"ro"`
	// Limits. Zero means "do not set this limit".
	CPUSeconds    uint64 `json:"cpu"`
	MemoryBytes   uint64 `json:"mem"`
	FileSizeBytes uint64 `json:"fsize"`
	MaxProcesses  uint64 `json:"nproc"`
}

// ErrUnsupported is returned by Available on platforms without an enforcement
// backend, and by the helper if it is asked to confine where it cannot.
var ErrUnsupported = errors.New("sandbox is not supported on this platform")

// Command builds a command that runs name with args under the given Spec, by
// re-execing this binary as the sandbox helper. env is the child environment;
// callers pass procenv.Minimal(...). The helper inherits it and carries it into
// the final exec.
func Command(ctx context.Context, self string, spec Spec, env []string, name string, args ...string) (*exec.Cmd, error) {
	encoded, err := json.Marshal(spec)
	if err != nil {
		return nil, err
	}

	helperArgs := []string{helperCommand, base64.StdEncoding.EncodeToString(encoded), "--", name}
	helperArgs = append(helperArgs, args...)

	cmd := exec.CommandContext(ctx, self, helperArgs...)
	cmd.Env = env
	return cmd, nil
}

// IsHelperInvocation reports whether os.Args describe a sandbox helper re-exec.
func IsHelperInvocation(args []string) bool {
	return len(args) >= 2 && args[1] == helperCommand
}

// RunHelper is the entry point for the re-execed helper process. It applies the
// restrictions to itself and then execs the target command, replacing itself.
// It never returns on success. On failure it returns an exit code for main.
func RunHelper(args []string) int {
	spec, name, rest, err := parseHelperArgs(args)
	if err != nil {
		fmt.Fprintln(os.Stderr, "sandbox:", err)
		return 2
	}

	if err := applyAndExec(spec, name, rest, procenv.Minimal()); err != nil {
		fmt.Fprintln(os.Stderr, "sandbox:", err)
		return 126
	}
	return 0 // unreachable: applyAndExec execs on success.
}

func parseHelperArgs(args []string) (Spec, string, []string, error) {
	// args: [self, helperCommand, <spec>, "--", name, rest...]
	if len(args) < 5 || args[1] != helperCommand || args[3] != "--" {
		return Spec{}, "", nil, errors.New("malformed helper invocation")
	}
	decoded, err := base64.StdEncoding.DecodeString(args[2])
	if err != nil {
		return Spec{}, "", nil, fmt.Errorf("decode spec: %w", err)
	}
	var spec Spec
	if err := json.Unmarshal(decoded, &spec); err != nil {
		return Spec{}, "", nil, fmt.Errorf("parse spec: %w", err)
	}
	return spec, args[4], args[5:], nil
}
