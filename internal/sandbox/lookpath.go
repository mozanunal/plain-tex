package sandbox

import (
	"os/exec"
	"path/filepath"

	"github.com/mozanunal/poly-txt/internal/procenv"
)

// exeLookPath resolves name against the PATH in env, so the helper finds the
// compiler using the same restricted environment it will exec it with.
func exeLookPath(name string, env []string) (string, error) {
	if filepath.IsAbs(name) {
		return name, nil
	}
	if pathEnv := procenv.Lookup(env, "PATH"); pathEnv != "" {
		resolved, err := lookPathIn(name, pathEnv)
		if err == nil {
			return resolved, nil
		}
	}
	return exec.LookPath(name)
}

func lookPathIn(name, pathEnv string) (string, error) {
	for _, dir := range filepath.SplitList(pathEnv) {
		if dir == "" {
			dir = "."
		}
		candidate := filepath.Join(dir, name)
		if info, err := osStat(candidate); err == nil && !info.IsDir() {
			return candidate, nil
		}
	}
	return "", exec.ErrNotFound
}
