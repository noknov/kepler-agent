//go:build !windows && !darwin && !linux && !freebsd && !openbsd && !netbsd && !dragonfly

package connections

import (
	"context"
	"fmt"
)

func lockConnectionFile(context.Context, string) (func(), error) {
	return nil, fmt.Errorf("connection file locking is unavailable on this platform")
}
