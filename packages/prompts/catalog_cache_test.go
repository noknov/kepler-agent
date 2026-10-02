package prompts

import (
	"os"
	"path/filepath"
	"sync"
	"testing"
)

func TestStaticSystemPromptIsMemoizedUntilReload(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "agent.md"), []byte("memoized system\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := LoadDirs(PublicDir, dir); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = LoadDirs(PublicDir) })

	first := StaticSystemPrompt()
	second := StaticSystemPrompt()
	if first != second {
		t.Fatalf("StaticSystemPrompt() should return cached value")
	}
	if first == "" {
		t.Fatal("StaticSystemPrompt() should not be empty")
	}

	other := t.TempDir()
	if err := os.WriteFile(filepath.Join(other, "agent.md"), []byte("reloaded system\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := LoadDirs(PublicDir, other); err != nil {
		t.Fatal(err)
	}
	reloaded := StaticSystemPrompt()
	if reloaded == first {
		t.Fatalf("StaticSystemPrompt() should rebuild after LoadDirs")
	}
}

func TestConcurrentReloadPublishesOneCompletePrompt(t *testing.T) {
	dirs := []string{t.TempDir(), t.TempDir()}
	expected := make(map[string]bool)
	for index, dir := range dirs {
		name := []string{"alpha", "beta"}[index]
		if err := os.Mkdir(filepath.Join(dir, "rules"), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, "system.md"), []byte(name+" system"), 0o644); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, "rules", "general.md"), []byte(name+" rules"), 0o644); err != nil {
			t.Fatal(err)
		}
		if err := LoadDirs(dir); err != nil {
			t.Fatal(err)
		}
		expected[StaticSystemPrompt()] = true
	}
	t.Cleanup(func() { _ = LoadDirs(PublicDir) })
	done := make(chan struct{})
	var readers sync.WaitGroup
	for index := 0; index < 4; index++ {
		readers.Add(1)
		go func() {
			defer readers.Done()
			for {
				select {
				case <-done:
					return
				default:
				}
				if got := StaticSystemPrompt(); !expected[got] {
					t.Errorf("mixed or empty catalog snapshot: %q", got)
					return
				}
			}
		}()
	}
	for index := 0; index < 40; index++ {
		if err := LoadDirs(dirs[index%len(dirs)]); err != nil {
			t.Error(err)
			break
		}
	}
	close(done)
	readers.Wait()
}
