package local

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"unicode/utf8"

	"github.com/noknov/kepler-agent/packages/agent/model"
	"github.com/noknov/kepler-agent/packages/agent/tool"
)

var safeArtifactName = regexp.MustCompile(`[^A-Za-z0-9._-]+`)
var artifactID = regexp.MustCompile(`^[a-f0-9]{64}\.json$`)

const artifactPageBytes = 12000

type ArtifactStore struct{ Root string }

func (s ArtifactStore) Put(ctx context.Context, scope tool.Scope, name string, content []byte) (model.Artifact, error) {
	if err := ctx.Err(); err != nil {
		return model.Artifact{}, err
	}
	if !safeID.MatchString(scope.SessionID) {
		return model.Artifact{}, fmt.Errorf("invalid session id")
	}
	name = safeArtifactName.ReplaceAllString(filepath.Base(name), "_")
	if name == "" || name == "." {
		name = "artifact.bin"
	}
	// Bind identity to the turn and payload: reused provider call IDs cannot
	// overwrite evidence from an earlier turn. Physical paths stay out of replies.
	identity, _ := json.Marshal([]string{scope.TurnID, name, fmt.Sprintf("%x", sha256.Sum256(content))})
	id := fmt.Sprintf("%x.json", sha256.Sum256(identity))
	if err := os.MkdirAll(s.Root, 0o700); err != nil {
		return model.Artifact{}, err
	}
	root, err := os.OpenRoot(s.Root)
	if err != nil {
		return model.Artifact{}, err
	}
	defer root.Close()
	directory := filepath.Join(scope.SessionID, "artifacts")
	if err := root.MkdirAll(directory, 0o700); err != nil {
		return model.Artifact{}, err
	}
	if err := root.WriteFile(filepath.Join(directory, id), content, 0o600); err != nil {
		return model.Artifact{}, err
	}
	return model.Artifact{ID: id, Name: name, MediaType: "application/json", URI: "artifact://" + scope.SessionID + "/" + id, SizeBytes: int64(len(content))}, nil
}

// ArtifactReadTool grants access only to artifacts in the current session,
// independently of workspace file permissions. It never accepts a host path.
type ArtifactReadTool struct{ Root string }

func (ArtifactReadTool) Descriptor() tool.Descriptor {
	return tool.Descriptor{Name: "artifact_read", Description: "Read a UTF-8 byte range from an artifact:// reference produced by a tool in this session. Follow next_offset to retrieve more evidence.", Effects: []tool.Effect{tool.EffectRead}, Parallel: true, Exposure: tool.ExposureEager,
		InputSchema: json.RawMessage(`{"type":"object","additionalProperties":false,"properties":{"uri":{"type":"string"},"offset":{"type":"integer","minimum":0},"limit":{"type":"integer","minimum":1,"maximum":12000}},"required":["uri"]}`)}
}

func (t ArtifactReadTool) Execute(ctx context.Context, call tool.Call) (tool.Result, error) {
	if err := ctx.Err(); err != nil {
		return tool.Result{}, err
	}
	var args struct {
		URI    string `json:"uri"`
		Offset int64  `json:"offset"`
		Limit  int    `json:"limit"`
	}
	if err := json.Unmarshal(call.Arguments, &args); err != nil {
		return tool.Result{}, err
	}
	ref, valid := strings.CutPrefix(args.URI, "artifact://")
	parts := strings.Split(ref, "/")
	if !valid || len(parts) != 2 || !safeID.MatchString(call.Scope.SessionID) || parts[0] != call.Scope.SessionID || !artifactID.MatchString(parts[1]) || args.Offset < 0 {
		return tool.Result{}, fmt.Errorf("invalid artifact reference or scope")
	}
	if args.Limit <= 0 || args.Limit > artifactPageBytes {
		args.Limit = artifactPageBytes
	}
	// OpenRoot confines symlinks as well as '..' traversal. Open the session's
	// artifact directory as its own root, never the whole state directory.
	root, err := os.OpenRoot(t.Root)
	if err != nil {
		return tool.Result{}, fmt.Errorf("artifact store unavailable")
	}
	defer root.Close()
	session, err := root.OpenRoot(filepath.Join(call.Scope.SessionID, "artifacts"))
	if err != nil {
		return tool.Result{}, fmt.Errorf("artifact unavailable in this session")
	}
	defer session.Close()
	file, err := session.Open(parts[1])
	if err != nil {
		return tool.Result{}, fmt.Errorf("artifact unavailable in this session")
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil || !info.Mode().IsRegular() {
		return tool.Result{}, fmt.Errorf("artifact is not a regular file")
	}
	if args.Offset > info.Size() {
		return tool.Result{}, fmt.Errorf("offset exceeds artifact length")
	}
	// A few extra bytes allow a caller-supplied offset to advance to a rune
	// boundary, while reads remain bounded independently of the artifact size.
	data := make([]byte, args.Limit+utf8.UTFMax)
	n, err := file.ReadAt(data, args.Offset)
	if err != nil && err != io.EOF {
		return tool.Result{}, fmt.Errorf("read artifact failed")
	}
	data = data[:n]
	start := 0
	for start < len(data) && !utf8.RuneStart(data[start]) {
		start++
	}
	end := min(start+args.Limit, len(data))
	for end > start && !utf8.Valid(data[start:end]) {
		end--
	}
	if end == start && args.Offset+int64(start) < info.Size() {
		return tool.Result{}, fmt.Errorf("limit is too small for one UTF-8 character")
	}
	next := args.Offset + int64(end)
	return tool.TextResult(fmt.Sprintf("Untrusted artifact bytes %d..%d/%d; next_offset=%d; eof=%t:\n%s", args.Offset+int64(start), next, info.Size(), next, next == info.Size(), data[start:end])), nil
}
