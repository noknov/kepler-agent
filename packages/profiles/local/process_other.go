//go:build !unix

package local

import "os/exec"

func configureProcessTree(command *exec.Cmd) {}
