package k8s

import (
	"testing"
	"time"
)

func TestParseClusterContext(t *testing.T) {
	def := Defaults{Project: "example-project", Region: "asia-southeast1", Cluster: "example-cluster"}
	p, l, c := parseClusterContext("", def)
	if p != "example-project" || l != "asia-southeast1" || c != "example-cluster" {
		t.Fatalf("defaults = %q %q %q", p, l, c)
	}
	p, l, c = parseClusterContext("gke_example-project_asia-southeast1_other", def)
	if c != "other" {
		t.Fatalf("gke context cluster = %q", c)
	}
}

func TestBuildGetPath(t *testing.T) {
	path, err := buildGetPath("deployment", "analytics", "gateway", false)
	if err != nil {
		t.Fatal(err)
	}
	if path != "/apis/apps/v1/namespaces/analytics/deployments/gateway" {
		t.Fatalf("path = %q", path)
	}
}

func TestParseDuration(t *testing.T) {
	d, err := parseDuration("5m")
	if err != nil || d != 5*time.Minute {
		t.Fatalf("parseDuration(5m) = %v %v", d, err)
	}
}
