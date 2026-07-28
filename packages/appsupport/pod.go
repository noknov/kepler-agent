package appsupport

import (
	"crypto/rand"
	"encoding/hex"
	"strings"

	"github.com/noknov/slack-copilot-agent/packages/infra/envutil"
)

func GeneratePodID() string {
	if v := strings.TrimSpace(envutil.Env("HOSTNAME", "")); v != "" {
		return v
	}
	var b [8]byte
	_, _ = rand.Read(b[:])
	return "pod-" + hex.EncodeToString(b[:])
}
