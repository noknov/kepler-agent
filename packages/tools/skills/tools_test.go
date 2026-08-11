package skills

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/noknov/slack-copilot-agent/packages/prompts"
	"github.com/noknov/slack-copilot-agent/packages/tools/registry"
	"github.com/noknov/slack-copilot-agent/packages/userprefs"
)

func TestLoadToolReturnsSkillBody(t *testing.T) {
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, "skills", "demo"), 0o755); err != nil {
		t.Fatal(err)
	}
	body := `---
name: demo
description: Use for demo tasks.
---

# Demo

Follow these detailed steps.
`
	if err := os.WriteFile(filepath.Join(dir, "skills", "demo", "SKILL.md"), []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := prompts.LoadDir(dir); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = prompts.LoadDirs(prompts.PublicDir) })

	result, err := LoadTool{}.Execute(context.Background(), json.RawMessage(`{"name":"demo"}`), registry.Runtime{})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(result.Content, "Follow these detailed steps.") {
		t.Fatalf("skill body missing:\n%s", result.Content)
	}
}

func TestLoadToolPrefersUserSkill(t *testing.T) {
	ctx := context.Background()
	store := &skillTestStore{asset: userprefs.Asset{
		UserID:      "U1",
		Kind:        userprefs.KindSkill,
		Name:        "demo",
		Description: "User override.",
		Content:     "Follow the user's detailed workflow.",
		Active:      true,
	}}

	result, err := (LoadTool{UserPrefs: store}).Execute(ctx, json.RawMessage(`{"name":"demo"}`), registry.Runtime{UserID: "U1"})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(result.Content, "User override.") || !strings.Contains(result.Content, "Slack user upload") {
		t.Fatalf("user skill body missing:\n%s", result.Content)
	}
}

type skillTestStore struct{ asset userprefs.Asset }

func (s *skillTestStore) GetSettings(_ context.Context, userID string) (userprefs.Settings, error) {
	return userprefs.Settings{UserID: userID, WebSearchEnabled: true}, nil
}
func (s *skillTestStore) SetWebSearchEnabled(context.Context, string, bool) error { return nil }
func (s *skillTestStore) ListAssets(_ context.Context, userID string, kind userprefs.AssetKind) ([]userprefs.Asset, error) {
	if s.asset.UserID == userID && s.asset.Kind == kind {
		return []userprefs.Asset{s.asset}, nil
	}
	return nil, nil
}
func (s *skillTestStore) UpsertAsset(_ context.Context, asset userprefs.Asset) (userprefs.Asset, error) {
	s.asset = asset
	return asset, nil
}
func (s *skillTestStore) DeleteAsset(context.Context, string, userprefs.AssetKind, string) error {
	return nil
}
func (s *skillTestStore) DeleteAssets(context.Context, string, userprefs.AssetKind) error { return nil }
