//go:build !unix

package local

import "os"

const workspaceReadFlags = os.O_RDONLY
