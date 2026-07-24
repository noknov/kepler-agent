package shell

import (
	"strings"
	"testing"
)

func TestValidateShellCommandAllowsOperationalReads(t *testing.T) {
	cases := []string{
		// kubectl
		`kubectl get pods -n mt-prod -l app=instagram-service`,
		`kubectl describe deployment instagram-service -n mt-prod`,
		`kubectl logs instagram-service-abc -n mt-prod --tail 100`,
		`kubectl top pods -n mt-prod`,
		`kubectl config get-contexts`,
		// gcloud
		`gcloud logging read 'severity>=ERROR' --project wati-gke --limit 10`,
		`gcloud container clusters describe mt-prod --region asia-southeast1`,
		`gcloud run services describe instagram-service --region asia-southeast1`,
		// gh
		`gh run list --repo ClareAI/devops-github-workflow --limit 5`,
		`gh run view 123456 --log-failed`,
		`gh pr list`,
		// git
		`git log --oneline -20`,
		`git -C /workspace/repo log --oneline -20`,
		`git --no-pager -C /workspace/repo show HEAD:README.md`,
		`git blame internal/app/tools.go`,
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

func TestValidateShellCommandBlocksWritesAndSecrets(t *testing.T) {
	cases := []string{
		// kubectl writes
		`kubectl delete pod x -n mt-prod`,
		`kubectl apply -f deploy.yaml`,
		`kubectl get secrets -n mt-prod`,
		`kubectl describe secret app-token -n mt-prod`,
		`kubectl exec pod -- env`,
		// gcloud writes
		`gcloud run services update instagram-service`,
		`gcloud container clusters get-credentials mt-prod`,
		// gh writes
		`gh workflow run deploy.yml`,
		`gh run cancel 123456`,
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
