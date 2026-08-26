package local

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"regexp"

	"github.com/noknov/kepler-agent/packages/agent/model"
	"github.com/noknov/kepler-agent/packages/agent/tool"
)

var safeArtifactName = regexp.MustCompile(`[^A-Za-z0-9._-]+`)

type ArtifactStore struct{ Root string }

func (s ArtifactStore) Put(_ context.Context, scope tool.Scope, name string, content []byte) (model.Artifact, error) {
	if !safeID.MatchString(scope.SessionID) {
		return model.Artifact{}, fmt.Errorf("invalid session id")
	}
	name = safeArtifactName.ReplaceAllString(filepath.Base(name), "_")
	if name == "" || name == "." {
		name = "artifact.bin"
	}
	directory := filepath.Join(s.Root, scope.SessionID, "artifacts")
	if err := os.MkdirAll(directory, 0o700); err != nil {
		return model.Artifact{}, err
	}
	path := filepath.Join(directory, name)
	if err := os.WriteFile(path, content, 0o600); err != nil {
		return model.Artifact{}, err
	}
	return model.Artifact{ID: name, Name: name, MediaType: "application/json", URI: path, SizeBytes: int64(len(content))}, nil
}
