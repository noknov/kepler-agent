package shell

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

func TestValidateShellCommandAllowsOperationalReads(t *testing.T) {
	cases := []string{
		// kubectl
		`kubectl get pods -n mt-prod -l app=instagram-service`,
		`kubectl describe deployment instagram-service -n mt-prod`,
		`kubectl logs instagram-service-abc -n mt-prod --tail 100`,
		`kubectl top pods -n mt-prod`,
		`kubectl config get-contexts`,
		`kubectl auth can-i get pods -n mt-prod`,
		`kubectl rollout status deployment/instagram-service -n mt-prod`,
		`kubectl rollout history deployment/instagram-service -n mt-prod`,
		// gcloud
		`gcloud logging read 'severity>=ERROR' --project wati-gke --limit 10`,
		`gcloud container clusters describe mt-prod --region asia-southeast1`,
		`gcloud run services describe instagram-service --region asia-southeast1`,
		// gh
		`gh run list --repo ClareAI/devops-github-workflow --limit 5`,
		`gh run view 123456 --log-failed`,
		`gh pr list`,
		`gh search prs "repo:ClareAI/wati-workflow-service review:required" --limit 10`,
		`gh search issues "repo:ClareAI/wati-workflow-service bug" --limit 10`,
		`gh api repos/ClareAI/wati-workflow-service/pulls/63/comments --paginate`,
		`gh api /repos/ClareAI/wati-workflow-service/issues/63/comments --jq '.[].body'`,
		`gh api repos/owner/repo/pulls/63/reviews --method GET`,
		`gh api repos/owner/repo/pulls/63/comments -X HEAD`,
		// git
		`git log --oneline -20`,
		`git -C /workspace/repo log --oneline -20`,
		`git --no-pager -C /workspace/repo show HEAD:README.md`,
		`git blame packages/slackbot/tools.go`,
		`git diff HEAD~1`,
		`git show HEAD:README.md`,
		`git status`,
		`git fetch origin`,
		`git grep "TODO" -- "*.go"`,
		// unix utilities
		`grep -r "error" /var/log/app.log`,
		`cat project/config.json`,
		`jq '.pods[].name' pods.json`,
		`date`,
		// pipelines
		`kubectl get pods -n mt-prod | grep web`,
		`git log --oneline | head -20`,
		`kubectl logs svc/web -n mt-prod | grep ERROR | tail -50`,
	}
	for _, cmd := range cases {
		if err := validateShellCommand(cmd); err != nil {
			t.Fatalf("validateShellCommand(%q) unexpected error: %v", cmd, err)
		}
	}
}

func TestShellGitLogDefaultsToFetchedOriginHEAD(t *testing.T) {
	root, work := shellTestRepo(t)
	first := runGitOutput(t, work, "rev-parse", "HEAD")

	if err := os.WriteFile(filepath.Join(work, "README.md"), []byte("hello\nfresh remote\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	runGit(t, work, "add", "README.md")
	runGit(t, work, "-c", "user.name=test", "-c", "user.email=test@example.com", "commit", "-m", "fresh remote")
	runGit(t, work, "push", "origin", "main")
	runGit(t, work, "reset", "--hard", strings.TrimSpace(first))

	tool := ReadOnlyTool{
		WorkspaceRoots: []string{root},
		Guard:          safety.NewCommandPolicy(),
		Timeout:        10 * time.Second,
	}
	result, err := tool.Execute(context.Background(), json.RawMessage(`{"command":"git -C `+work+` log --format=%s -1"}`), registry.Runtime{})
	if err != nil {
		t.Fatal(err)
	}
	if strings.TrimSpace(result.Content) != "fresh remote" {
		t.Fatalf("shell git log content = %q, want remote origin/HEAD tip", result.Content)
	}
}

func TestNormalizeBareGitLogKeepsExplicitRef(t *testing.T) {
	got := normalizeGitDefaultRef([]string{"git", "-C", "/workspace/repo", "log", "origin/main", "--oneline", "-1"})
	if strings.Join(got, " ") != "git -C /workspace/repo log origin/main --oneline -1" {
		t.Fatalf("explicit ref was changed: %#v", got)
	}

	got = normalizeGitDefaultRef([]string{"git", "-C", "/workspace/repo", "log", "--oneline", "-1"})
	if strings.Join(got, " ") != "git -C /workspace/repo log origin/HEAD --oneline -1" {
		t.Fatalf("bare log was not normalized: %#v", got)
	}
}

func TestNormalizeGitGrepAndShowUseOriginHEAD(t *testing.T) {
	got := normalizeGitDefaultRef([]string{"git", "-C", "/workspace/repo", "grep", "TODO", "--", "*.go"})
	if strings.Join(got, " ") != "git -C /workspace/repo grep TODO origin/HEAD -- *.go" {
		t.Fatalf("bare git grep was not normalized: %#v", got)
	}

	got = normalizeGitDefaultRef([]string{"git", "-C", "/workspace/repo", "show", "HEAD:README.md"})
	if strings.Join(got, " ") != "git -C /workspace/repo show origin/HEAD:README.md" {
		t.Fatalf("git show HEAD:path was not normalized: %#v", got)
	}
}

func TestShellBlocksWorkingTreeSourceReads(t *testing.T) {
	root, work := shellTestRepo(t)
	tool := ReadOnlyTool{
		WorkspaceRoots: []string{root},
		Guard:          safety.NewCommandPolicy(),
		Timeout:        10 * time.Second,
	}
	for _, command := range []string{
		`grep -r hello ` + work,
		`rg hello ` + work,
		`cat ` + filepath.Join(work, "README.md"),
		`rg hello`,
	} {
		_, err := tool.Execute(context.Background(), json.RawMessage(`{"command":"`+command+`"}`), registry.Runtime{})
		if err == nil || !strings.Contains(err.Error(), "reads the local checkout") {
			t.Fatalf("Execute(%q) error = %v, want local checkout block", command, err)
		}
	}
}

func TestValidateShellCommandBlocksWritesAndSecrets(t *testing.T) {
	cases := []string{
		// kubectl writes
		`kubectl delete pod x -n mt-prod`,
		`kubectl apply -f deploy.yaml`,
		`kubectl get secrets -n mt-prod`,
		`kubectl describe secret app-token -n mt-prod`,
		`kubectl exec pod -- env`,
		`kubectl rollout restart deployment/instagram-service -n mt-prod`,
		`kubectl rollout undo deployment/instagram-service -n mt-prod`,
		`kubectl auth reconcile -f roles.yaml`,
		// gcloud writes
		`gcloud run services update instagram-service`,
		`gcloud container clusters get-credentials mt-prod`,
		// gh writes
		`gh workflow run deploy.yml`,
		`gh run cancel 123456`,
		`gh api repos/owner/repo/issues -f title=test`,
		`gh api repos/owner/repo/issues --method POST`,
		`gh api repos/owner/repo/issues -XPATCH`,
		`gh api graphql -f query='query { viewer { login } }'`,
		`gh api repos/owner/repo/actions/secrets`,
		// git writes
		`git push origin main`,
		`git commit -m "foo"`,
		`git rebase origin/main`,
		`git clean -fd`,
		`git -C /workspace/repo checkout main`,
		`git --no-pager -C /workspace/repo reset --hard`,
		`git --git-dir=/tmp/repo/.git log`,
		`git -c core.sshCommand=sh log`,
		// disallowed bins
		`bash -c date`,
		`sh -c whoami`,
		`rm -rf /tmp/foo`,
		// chaining operators
		`date; whoami`,
		`kubectl get pods && kubectl get nodes`,
		`gcloud logging read "$(cat filter)"`,
		`kubectl get pods > pods.txt`,
	}
	for _, cmd := range cases {
		if err := validateShellCommand(cmd); err == nil {
			t.Fatalf("validateShellCommand(%q) succeeded, want block", cmd)
		}
	}
}

func TestValidateShellCommandAllowsOperatorsInsideQuotedArguments(t *testing.T) {
	// Pipes in jq filters are arguments, not pipeline operators.
	cases := []string{
		`jq '.items[] | .name' data.json`,
	}
	for _, cmd := range cases {
		if err := validateShellCommand(cmd); err != nil {
			t.Fatalf("validateShellCommand(%q) unexpected error: %v", cmd, err)
		}
	}
}

func TestValidateShellCommandBlocksBackgroundExecution(t *testing.T) {
	// Unquoted '&' must still be blocked regardless of position.
	cases := []string{
		`rg foo &`,
		`sleep 5 &`,
		`date & echo done`,
		// '&&' outside quotes must still be blocked
		`rg foo && echo ok`,
		`python3 -c 'print(1)'`,
		`tee /tmp/out`,
		`curl https://example.com`,
		`find . -name "*.go"`,
		`kubectl config use-context prod`,
	}
	for _, cmd := range cases {
		if err := validateShellCommand(cmd); err == nil {
			t.Fatalf("validateShellCommand(%q) succeeded, want block (background/chain)", cmd)
		}
	}
}

func TestValidateShellCommandErrorMessagesMatchAllowlist(t *testing.T) {
	err := validateShellCommand(`find . -name "*.go"`)
	if err == nil || !strings.Contains(err.Error(), "use rg or a repository-specific code tool") {
		t.Fatalf("find error = %v, want rg guidance", err)
	}

	err = validateShellCommand(`python3 -c 'print(1)'`)
	if err == nil {
		t.Fatal("python3 succeeded, want block")
	}
	for _, unsupported := range []string{"find", "curl", "awk/sed"} {
		if strings.Contains(err.Error(), unsupported) {
			t.Fatalf("unknown-command error mentions unsupported tool %q: %v", unsupported, err)
		}
	}
}

func TestValidatePathsAllowsTmp(t *testing.T) {
	tool := ReadOnlyTool{WorkspaceRoots: []string{"/workspace/proj"}}
	cases := []string{
		`curl -o /tmp/response.json https://api.example.com`,
		`cat /tmp/auth.json`,
		`find /tmp -name "*.json"`,
	}
	for _, cmd := range cases {
		if err := tool.validatePaths(cmd); err != nil {
			t.Fatalf("validatePaths(%q) unexpected error: %v", cmd, err)
		}
	}
}

func TestValidatePathsBlocksOutsideWorkspace(t *testing.T) {
	tool := ReadOnlyTool{WorkspaceRoots: []string{"/workspace/proj"}}
	cases := []string{
		`cat /etc/passwd`,
		`ls /Users/other/repo`,
		`cat /workspace/other-proj/secret.go`,
	}
	for _, cmd := range cases {
		if err := tool.validatePaths(cmd); err == nil {
			t.Fatalf("validatePaths(%q) succeeded, want block (outside workspace)", cmd)
		}
	}
}

func shellTestRepo(t *testing.T) (string, string) {
	t.Helper()
	gitcache.ResetForTest()
	root := t.TempDir()
	origin := filepath.Join(root, "origin.git")
	work := filepath.Join(root, "work")
	runGit(t, root, "init", "--bare", origin)
	runGit(t, origin, "symbolic-ref", "HEAD", "refs/heads/main")
	runGit(t, root, "init", "-b", "main", work)
	if err := os.WriteFile(filepath.Join(work, "README.md"), []byte("hello\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	runGit(t, work, "add", "README.md")
	runGit(t, work, "-c", "user.name=test", "-c", "user.email=test@example.com", "commit", "-m", "init")
	runGit(t, work, "remote", "add", "origin", origin)
	runGit(t, work, "push", "-u", "origin", "main")
	runGit(t, work, "fetch", "origin")
	runGit(t, work, "symbolic-ref", "refs/remotes/origin/HEAD", "refs/remotes/origin/main")
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

func runGitOutput(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %s failed: %v\n%s", strings.Join(args, " "), err, strings.TrimSpace(string(out)))
	}
	return string(out)
}
