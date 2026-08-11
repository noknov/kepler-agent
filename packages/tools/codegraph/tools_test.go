package codegraph

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/noknov/slack-copilot-agent/packages/safety"
	"github.com/noknov/slack-copilot-agent/packages/toolkit/gitcache"
	"github.com/noknov/slack-copilot-agent/packages/tools/registry"
)

func TestCodegraphFindsGoDefinitionsAndCallees(t *testing.T) {
	root, work := testRepo(t, map[string]string{
		"go.mod": "module example.com/app\n\ngo 1.25\n",
		"app.go": `package app

type Loader interface {
	LoadPosts()
}

type Service struct{}

func (Service) LoadPosts() {}

func AddCommentRoutes() {
	getPostList()
}

func getPostList() {
	var loader Loader = Service{}
	loader.LoadPosts()
}
`,
	})
	base := Base{Paths: safety.WorkspacePolicy{Roots: []string{root}}, Timeout: 10 * time.Second}
	rt := registry.Runtime{Cache: registry.NewRuntimeCache()}

	def, err := (DefinitionTool{Base: base}).Execute(context.Background(), json.RawMessage(`{"repo":"`+work+`","branch":"main","symbol":"AddCommentRoutes"}`), rt)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(def.Content, "go_func AddCommentRoutes") {
		t.Fatalf("definition content = %q", def.Content)
	}

	callees, err := (CalleesTool{Base: base}).Execute(context.Background(), json.RawMessage(`{"repo":"`+work+`","branch":"main","symbol":"AddCommentRoutes"}`), rt)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(callees.Content, "AddCommentRoutes -> getPostList") {
		t.Fatalf("callees content = %q", callees.Content)
	}

	impls, err := (ImplementationsTool{Base: base}).Execute(context.Background(), json.RawMessage(`{"repo":"`+work+`","branch":"main","symbol":"Loader"}`), rt)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(impls.Content, "go_type Service") {
		t.Fatalf("implementations content = %q", impls.Content)
	}

	refs, err := (ReferencesTool{Base: base}).Execute(context.Background(), json.RawMessage(`{"repo":"`+work+`","branch":"main","symbol":"Loader"}`), rt)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(refs.Content, "Loader context=getPostList") {
		t.Fatalf("references content = %q", refs.Content)
	}

	callgraph, err := (CallgraphTool{Base: base}).Execute(context.Background(), json.RawMessage(`{"repo":"`+work+`","branch":"main","filter":"LoadPosts"}`), rt)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(callgraph.Content, "getPostList -> LoadPosts") {
		t.Fatalf("callgraph content = %q", callgraph.Content)
	}
}

func TestCodegraphFindsCSharpDefinitionsAndCallers(t *testing.T) {
	root, work := testRepo(t, map[string]string{
		"Controllers/CommentController.cs": `namespace Messaging.Instagram.Controllers;

public interface IPostLoader {}

public class CommentController : IPostLoader
{
    public void GetPostList()
    {
        LoadPosts();
    }

    private void LoadPosts() {}
}
`,
	})
	base := Base{Paths: safety.WorkspacePolicy{Roots: []string{root}}, Timeout: 10 * time.Second}
	rt := registry.Runtime{Cache: registry.NewRuntimeCache()}

	def, err := (DefinitionTool{Base: base}).Execute(context.Background(), json.RawMessage(`{"repo":"`+work+`","branch":"main","symbol":"CommentController.GetPostList"}`), rt)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(def.Content, "CommentController.GetPostList") {
		t.Fatalf("definition content = %q", def.Content)
	}

	callers, err := (CallersTool{Base: base}).Execute(context.Background(), json.RawMessage(`{"repo":"`+work+`","branch":"main","symbol":"LoadPosts"}`), rt)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(callers.Content, "CommentController.GetPostList -> LoadPosts") {
		t.Fatalf("callers content = %q", callers.Content)
	}

	impls, err := (ImplementationsTool{Base: base}).Execute(context.Background(), json.RawMessage(`{"repo":"`+work+`","branch":"main","symbol":"IPostLoader"}`), rt)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(impls.Content, "cs_class Messaging.Instagram.Controllers.CommentController") {
		t.Fatalf("implementations content = %q", impls.Content)
	}
}

func testRepo(t *testing.T, files map[string]string) (string, string) {
	t.Helper()
	gitcache.ResetForTest()
	root := t.TempDir()
	origin := filepath.Join(root, "origin.git")
	work := filepath.Join(root, "work")
	runGit(t, root, "init", "--bare", origin)
	runGit(t, root, "init", "-b", "main", work)
	for path, content := range files {
		fullPath := filepath.Join(work, path)
		if err := os.MkdirAll(filepath.Dir(fullPath), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(fullPath, []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	runGit(t, work, "add", ".")
	runGit(t, work, "-c", "user.name=test", "-c", "user.email=test@example.com", "commit", "-m", "init")
	runGit(t, work, "remote", "add", "origin", origin)
	runGit(t, work, "push", "origin", "main")
	return root, work
}

func runGit(t *testing.T, dir string, args ...string) {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %s failed: %v\n%s", strings.Join(args, " "), err, strings.TrimSpace(string(out)))
	}
}
