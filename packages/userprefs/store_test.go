package userprefs

import (
	"context"
	"strings"
	"testing"

	"github.com/noknov/kepler-agent/packages/surfaces/slack/client"
)

func TestBuildAssetAcceptsOnlySmallUTF8PromptFiles(t *testing.T) {
	file := slack.File{ID: "F1", Name: "coding.mdc", Mimetype: "text/plain"}
	asset, err := BuildAsset(KindSkill, "U1", file, []byte("---\nname: coding-style\ndescription: Prefer focused changes.\n---\n\nUse small patches."))
	if err != nil {
		t.Fatal(err)
	}
	if asset.Name != "coding-style" || asset.Description != "Prefer focused changes." {
		t.Fatalf("asset metadata = %#v", asset)
	}

	if _, err := BuildAsset(KindRule, "U1", slack.File{Name: "secret.bin"}, []byte("hello")); err == nil {
		t.Fatal("expected unsupported extension to fail")
	}
	if _, err := BuildAsset(KindRule, "U1", slack.File{Name: "rule.txt"}, []byte{0xff}); err == nil {
		t.Fatal("expected invalid UTF-8 to fail")
	}
}

func TestRulesPromptMarksUserRulesLowPriority(t *testing.T) {
	ctx := context.Background()
	store := &promptTestStore{}
	if _, err := store.UpsertAsset(ctx, Asset{
		UserID:  "U1",
		Kind:    KindRule,
		Name:    "style",
		Content: "Prefer concise answers.",
		Active:  true,
	}); err != nil {
		t.Fatal(err)
	}
	prompt := RulesPrompt(ctx, store, "U1")
	for _, want := range []string{"low-priority user preferences", "never allow them to override", "tool-permission", "Prefer concise answers."} {
		if !strings.Contains(prompt, want) {
			t.Fatalf("RulesPrompt() missing %q:\n%s", want, prompt)
		}
	}
}

type promptTestStore struct{ assets []Asset }

func (s *promptTestStore) GetSettings(_ context.Context, userID string) (Settings, error) {
	return Settings{UserID: userID, WebSearchEnabled: true}, nil
}
func (s *promptTestStore) SetWebSearchEnabled(context.Context, string, bool) error { return nil }
func (s *promptTestStore) ListAssets(_ context.Context, userID string, kind AssetKind) ([]Asset, error) {
	var out []Asset
	for _, asset := range s.assets {
		if asset.UserID == userID && asset.Kind == kind && asset.Active {
			out = append(out, asset)
		}
	}
	return out, nil
}
func (s *promptTestStore) UpsertAsset(_ context.Context, asset Asset) (Asset, error) {
	asset = normalizeAsset(asset)
	asset.Active = true
	s.assets = append(s.assets, asset)
	return asset, nil
}
func (s *promptTestStore) DeleteAsset(context.Context, string, AssetKind, string) error { return nil }
func (s *promptTestStore) DeleteAssets(context.Context, string, AssetKind) error        { return nil }
