//go:build !linux

package sandbox

// Available reports that no enforcement backend exists on this platform.
func Available() bool { return false }

func applyAndExec(spec Spec, name string, args []string, env []string) error {
	return ErrUnsupported
}
