package gcp

import (
	"context"
	"fmt"
	"os/exec"
	"strings"
	"time"

	"github.com/noknov/kepler-agent/packages/agent/tool"
)

// LocalTokenSource resolves GCP API credentials from the local gcloud CLI
// (application-default or user credentials mounted into the worker container).
type LocalTokenSource struct {
	GCloudPath string
	Defaults   Defaults
	Timeout    time.Duration
}

func (s LocalTokenSource) Resolve(ctx context.Context, _ tool.Call) (Client, error) {
	path := strings.TrimSpace(s.GCloudPath)
	if path == "" {
		path = "gcloud"
	}
	timeout := s.Timeout
	if timeout <= 0 {
		timeout = 15 * time.Second
	}
	cmdCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	cmd := exec.CommandContext(cmdCtx, path, "auth", "print-access-token")
	out, err := cmd.Output()
	if err != nil {
		return Client{}, fmt.Errorf("gcloud auth print-access-token failed: %w (run gcloud auth login or mount valid ~/.config/gcloud)", err)
	}
	token := strings.TrimSpace(string(out))
	if token == "" {
		return Client{}, fmt.Errorf("gcloud returned an empty access token")
	}
	return Client{AccessToken: token, Defaults: s.Defaults}, nil
}
