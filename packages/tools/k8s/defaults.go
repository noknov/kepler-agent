package k8s

import (
	"strings"
	"time"
)

// Defaults holds deployment-wide GKE defaults.
type Defaults struct {
	Project   string
	Region    string
	Cluster   string
	Namespace string
}

// ClusterTarget identifies a GKE cluster for API calls.
type ClusterTarget struct {
	Project   string
	Location  string
	Cluster   string
	Namespace string
}

func resolveClusterTarget(contextArg string, defaults Defaults, namespaceArg string) (ClusterTarget, error) {
	project, location, cluster := parseClusterContext(contextArg, defaults)
	if strings.TrimSpace(project) == "" || strings.TrimSpace(cluster) == "" {
		return ClusterTarget{}, errClusterRequired
	}
	ns := strings.TrimSpace(namespaceArg)
	if ns == "" {
		ns = strings.TrimSpace(defaults.Namespace)
	}
	return ClusterTarget{
		Project:   project,
		Location:  location,
		Cluster:   cluster,
		Namespace: ns,
	}, nil
}

// parseClusterContext resolves project, location (region/zone), and cluster name.
// Supports kubectl-style GKE contexts: gke_PROJECT_LOCATION_CLUSTER.
func parseClusterContext(contextArg string, defaults Defaults) (project, location, cluster string) {
	contextArg = strings.TrimSpace(contextArg)
	if contextArg == "" {
		return strings.TrimSpace(defaults.Project), strings.TrimSpace(defaults.Region), strings.TrimSpace(defaults.Cluster)
	}
	if strings.HasPrefix(contextArg, "gke_") {
		rest := strings.TrimPrefix(contextArg, "gke_")
		parts := strings.SplitN(rest, "_", 3)
		if len(parts) == 3 {
			return parts[0], parts[1], parts[2]
		}
	}
	// Treat bare name as cluster override within configured project/region.
	return strings.TrimSpace(defaults.Project), strings.TrimSpace(defaults.Region), contextArg
}

func (t ClusterTarget) timeout(defaultTimeout time.Duration) time.Duration {
	if defaultTimeout > 0 {
		return defaultTimeout
	}
	return 30 * time.Second
}
