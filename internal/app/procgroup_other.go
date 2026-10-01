//go:build !unix

package app

import "os/exec"

func configureProcessGroup(cmd *exec.Cmd) {}
