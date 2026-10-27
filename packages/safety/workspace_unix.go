//go:build unix

package safety

import (
	"os"
	"syscall"
)

// Nonblocking open lets the regular-file check reject FIFOs before a writer
// connects. It has no effect on regular-file reads.
const workspaceReadFlags = os.O_RDONLY | syscall.O_NONBLOCK
