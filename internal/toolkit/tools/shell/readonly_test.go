package shell

import (
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
		`kubectl config use-context gke_wati-gke_asia-southeast1_mt-pubsub`,
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
		`git blame internal/app/tools.go`,
		`git diff HEAD~1`,
		`git show HEAD:README.md`,
		`git status`,
		`git fetch origin`,
		`git grep "TODO" -- "*.go"`,
		// unix utilities
		`grep -r "error" /var/log/app.log`,
		`find /tmp -name "*.json"`,
		`cat /etc/hostname`,
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

func TestValidateShellCommandAllowsAmpersandInURL(t *testing.T) {
	// '&' is a common query-string separator in URLs. When the URL is
	// properly quoted, stripQuotedStrings removes it before operator checking.
	cases := []string{
		`curl 'https://api.example.com/v1/query?foo=1&bar=2&baz=3'`,
		`curl -X POST 'https://auth.watiapp.io/login?clientId=x&redirect=y'`,
		`curl -H 'Content-Type: application/json' 'https://host/path?a=1&b=2'`,
		// semicolon inside a jq filter (single-quoted) must also be allowed
		`jq '.items[] | select(.name; "foo")' data.json`,
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
		`curl https://example.com &`,
		`sleep 5 &`,
		`date & echo done`,
		// '&&' outside quotes must still be blocked
		`curl https://example.com && echo ok`,
	}
	for _, cmd := range cases {
		if err := validateShellCommand(cmd); err == nil {
			t.Fatalf("validateShellCommand(%q) succeeded, want block (background/chain)", cmd)
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
