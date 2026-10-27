package prompts

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestCatalogRejectsInvalidOverlaysWithoutPublishingPartialState(t *testing.T) {
	for _, tc := range []struct {
		name, path, body string
		directory        bool
	}{
		{"invalid JSON", "tools.json", `{"demo":{"description":"changed"},`, false},
		{"invalid skill", "skills/demo/SKILL.md", "---\nname: [broken\n---\nbody", false},
		{"unreadable file kind", "system.md", "", true},
		{"empty JSON", "tools.json", "", false},
		{"invalid runtime JSON", "runtime.json", "[", false},
		{"invalid rules directory", "rules", "not a directory", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if err := LoadDirs(PublicDir); err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = LoadDirs(PublicDir) })
			before := StaticSystemPrompt()
			dir := t.TempDir()
			path := filepath.Join(dir, tc.path)
			if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
				t.Fatal(err)
			}
			if tc.directory {
				if err := os.Mkdir(path, 0700); err != nil {
					t.Fatal(err)
				}
			} else if err := os.WriteFile(path, []byte(tc.body), 0600); err != nil {
				t.Fatal(err)
			}
			if err := LoadDirs(dir); err == nil || !strings.Contains(err.Error(), tc.path) {
				t.Errorf("invalid overlay accepted or source not identified: %v", err)
			}
			if StaticSystemPrompt() != before {
				t.Error("failed overlay replaced active prompt state")
			}
		})
	}
}

func TestCatalogDoesNotSearchAncestorsForMissingRelativeOverlay(t *testing.T) {
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, "overlay"), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "overlay/system.md"), []byte("wrong ancestor prompt"), 0600); err != nil {
		t.Fatal(err)
	}
	cwd := filepath.Join(root, "nested")
	if err := os.Mkdir(cwd, 0700); err != nil {
		t.Fatal(err)
	}
	t.Chdir(cwd)
	t.Cleanup(func() { _ = LoadDirs(PublicDir) })
	if err := LoadDirs("overlay"); err == nil {
		t.Fatal("resolved explicit relative overlay against an ancestor")
	}
}

func TestAbsoluteCatalogPathIsNotAnEmbeddedCatalogAlias(t *testing.T) {
	dir := filepath.Join(t.TempDir(), filepath.FromSlash(PublicDir))
	if err := os.MkdirAll(dir, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "system.md"), []byte("actual configured prompt"), 0600); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = LoadDirs(PublicDir) })
	if err := LoadDirs(dir); err != nil {
		t.Fatal(err)
	}
	if System("") != "actual configured prompt" {
		t.Fatal("absolute directory was replaced by embedded defaults")
	}
}

func TestLoadFromEnvReportsMissingConfiguredCatalog(t *testing.T) {
	t.Setenv("PROMPT_DIR", filepath.Join(t.TempDir(), "absent"))
	if err := LoadFromEnv(); err == nil {
		t.Fatal("missing configured catalog accepted")
	}
	t.Cleanup(func() { _ = LoadDirs(PublicDir) })
}
