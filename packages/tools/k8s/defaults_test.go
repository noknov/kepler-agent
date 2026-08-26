package k8s

import (
	"testing"
	"time"
)

func TestParseClusterContext(t *testing.T) {
	def := Defaults{Project: "wati-gke", Region: "asia-southeast1", Cluster: "mt-pubsub"}
	p, l, c := parseClusterContext("", def)
	if p != "wati-gke" || l != "asia-southeast1" || c != "mt-pubsub" {
		t.Fatalf("defaults = %q %q %q", p, l, c)
	}
	p, l, c = parseClusterContext("gke_wati-gke_asia-southeast1_other", def)
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
