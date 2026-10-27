//go:build !unix

package safety

import "os"

const workspaceReadFlags = os.O_RDONLY
