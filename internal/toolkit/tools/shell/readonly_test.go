package shell

import "testing"

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
