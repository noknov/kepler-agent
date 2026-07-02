package shell

import "testing"

func TestValidateReadOnlyCommandAllowsOperationalReads(t *testing.T) {
	cases := []string{
		`gcloud logging read 'severity>=ERROR' --project wati-gke --limit 10`,
		`gcloud container clusters describe mt-prod --region asia-southeast1`,
		`gcloud run services describe instagram-service --region asia-southeast1`,
		`kubectl get pods -n mt-prod -l app=instagram-service`,
		`kubectl describe deployment instagram-service -n mt-prod`,
		`kubectl logs instagram-service-abc -n mt-prod --tail 100`,
		`kubectl top pods -n mt-prod`,
		`gh run list --repo ClareAI/devops-github-workflow --limit 5`,
		`gh run view 123456 --log-failed`,
		`date`,
	}
	for _, command := range cases {
		argv, err := splitCommand(command)
		if err != nil {
			t.Fatalf("splitCommand(%q) error = %v", command, err)
		}
		if err := validateReadOnlyCommand(argv); err != nil {
			t.Fatalf("validateReadOnlyCommand(%q) error = %v", command, err)
		}
	}
}

func TestValidateReadOnlyCommandBlocksWritesAndSecrets(t *testing.T) {
	cases := []string{
		`kubectl delete pod x -n mt-prod`,
		`kubectl apply -f deploy.yaml`,
		`kubectl get secrets -n mt-prod`,
		`kubectl describe secret app-token -n mt-prod`,
		`kubectl exec pod -- env`,
		`gcloud container clusters get-credentials mt-prod`,
		`gcloud run services update instagram-service`,
		`gh workflow run deploy.yml`,
		`gh run cancel 123456`,
		`bash -lc date`,
	}
	for _, command := range cases {
		argv, err := splitCommand(command)
		if err != nil {
			t.Fatalf("splitCommand(%q) error = %v", command, err)
		}
		if err := validateReadOnlyCommand(argv); err == nil {
			t.Fatalf("validateReadOnlyCommand(%q) succeeded, want block", command)
		}
	}
}

func TestSplitCommandRejectsShellSyntax(t *testing.T) {
	cases := []string{
		`kubectl get pods | grep api`,
		`date; whoami`,
		`gcloud logging read "$(cat filter)"`,
		`kubectl get pods > pods.txt`,
	}
	for _, command := range cases {
		if _, err := splitCommand(command); err == nil {
			t.Fatalf("splitCommand(%q) succeeded, want error", command)
		}
	}
}
