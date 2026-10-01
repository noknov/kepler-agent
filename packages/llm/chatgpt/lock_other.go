//go:build !darwin && !linux && !freebsd && !openbsd && !netbsd && !dragonfly

package chatgpt

import (
	"context"
	"errors"
)

func lock(context.Context, string) (func(), error) {
	return nil, errors.New("ChatGPT operator sessions require a Unix host")
}
