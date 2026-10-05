//go:build linux

package sandbox

import (
	"fmt"
	"syscall"

	"github.com/landlock-lsm/go-landlock/landlock"
	"golang.org/x/sys/unix"
)

// Available reports whether the kernel actually supports Landlock, by asking it
// for the supported ABI version. This is the reliable signal: reading
// /sys/kernel/security/lsm fails in many containers where securityfs is not
// mounted even though Landlock enforcement works.
func Available() bool {
	version, _, errno := unix.Syscall(
		unix.SYS_LANDLOCK_CREATE_RULESET,
		0, 0,
		landlockCreateRulesetVersion,
	)
	return errno == 0 && int(version) >= 1
}

// LANDLOCK_CREATE_RULESET_VERSION: ask for the ABI version instead of creating a
// ruleset.
const landlockCreateRulesetVersion = 1 << 0

func applyAndExec(spec Spec, name string, args []string, env []string) error {
	path, err := exeLookPath(name, env)
	if err != nil {
		return err
	}

	if err := setResourceLimits(spec); err != nil {
		return err
	}
	if err := applyLandlock(spec); err != nil {
		return err
	}

	argv := append([]string{path}, args...)
	// Exec replaces this process, so the restrictions above stay in force and
	// there is no sandboxed-but-still-poly-txt process left behind.
	return syscall.Exec(path, argv, env)
}

func setResourceLimits(spec Spec) error {
	limits := []struct {
		resource int
		value    uint64
	}{
		{unix.RLIMIT_CPU, spec.CPUSeconds},
		{unix.RLIMIT_AS, spec.MemoryBytes},
		{unix.RLIMIT_FSIZE, spec.FileSizeBytes},
		{unix.RLIMIT_NPROC, spec.MaxProcesses},
	}
	for _, limit := range limits {
		if limit.value == 0 {
			continue
		}
		rlimit := &unix.Rlimit{Cur: limit.value, Max: limit.value}
		if err := unix.Setrlimit(limit.resource, rlimit); err != nil {
			return fmt.Errorf("setrlimit %d: %w", limit.resource, err)
		}
	}
	return nil
}

func applyLandlock(spec Spec) error {
	rules := make([]landlock.Rule, 0, len(spec.ReadWrite)+len(spec.ReadOnly))
	for _, dir := range spec.ReadOnly {
		rules = append(rules, landlock.RODirs(dir).IgnoreIfMissing())
	}
	for _, dir := range spec.ReadWrite {
		rules = append(rules, landlock.RWDirs(dir).IgnoreIfMissing())
	}

	// BestEffort downgrades to the strongest subset the running kernel supports,
	// so an older kernel still gets file confinement even without newer rights.
	if err := landlock.V5.BestEffort().RestrictPaths(rules...); err != nil {
		return fmt.Errorf("landlock restrict: %w", err)
	}
	return nil
}
