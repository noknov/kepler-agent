package k8s

import (
	"encoding/json"
	"fmt"
	"net/url"
	"strings"
	"time"
)

func formatPodsWide(data []byte) (string, error) {
	var list struct {
		Items []struct {
			Metadata struct {
				Name              string `json:"name"`
				Namespace         string `json:"namespace"`
				CreationTimestamp time.Time `json:"creationTimestamp"`
			} `json:"metadata"`
			Status struct {
				Phase             string `json:"phase"`
				ContainerStatuses []struct {
					Ready        bool `json:"ready"`
					RestartCount int  `json:"restartCount"`
				} `json:"containerStatuses"`
			} `json:"status"`
			Spec struct {
				NodeName string `json:"nodeName"`
			} `json:"spec"`
		} `json:"items"`
	}
	if err := json.Unmarshal(data, &list); err != nil {
		return string(data), nil
	}
	var lines []string
	lines = append(lines, "NAME\tNAMESPACE\tREADY\tSTATUS\tRESTARTS\tAGE\tNODE")
	for _, pod := range list.Items {
		ready := 0
		total := len(pod.Status.ContainerStatuses)
		restarts := 0
		for _, cs := range pod.Status.ContainerStatuses {
			if cs.Ready {
				ready++
			}
			restarts += cs.RestartCount
		}
		readyStr := fmt.Sprintf("%d/%d", ready, total)
		age := formatAge(pod.Metadata.CreationTimestamp)
		lines = append(lines, fmt.Sprintf("%s\t%s\t%s\t%s\t%d\t%s\t%s",
			pod.Metadata.Name,
			pod.Metadata.Namespace,
			readyStr,
			pod.Status.Phase,
			restarts,
			age,
			pod.Spec.NodeName,
		))
	}
	return strings.Join(lines, "\n"), nil
}

func formatAge(ts time.Time) string {
	if ts.IsZero() {
		return ""
	}
	d := time.Since(ts)
	switch {
	case d < time.Minute:
		return fmt.Sprintf("%ds", int(d.Seconds()))
	case d < time.Hour:
		return fmt.Sprintf("%dm", int(d.Minutes()))
	case d < 24*time.Hour:
		return fmt.Sprintf("%dh", int(d.Hours()))
	default:
		return fmt.Sprintf("%dd", int(d.Hours()/24))
	}
}

func formatRolloutStatus(data []byte, kind, name string) (string, error) {
	var deploy struct {
		Status struct {
			Replicas            int32 `json:"replicas"`
			UpdatedReplicas     int32 `json:"updatedReplicas"`
			ReadyReplicas       int32 `json:"readyReplicas"`
			AvailableReplicas   int32 `json:"availableReplicas"`
			UnavailableReplicas int32 `json:"unavailableReplicas"`
			Conditions          []struct {
				Type    string `json:"type"`
				Status  string `json:"status"`
				Reason  string `json:"reason"`
				Message string `json:"message"`
			} `json:"conditions"`
		} `json:"status"`
	}
	if err := json.Unmarshal(data, &deploy); err != nil {
		return string(data), nil
	}
	var lines []string
	lines = append(lines, fmt.Sprintf("%s/%s rollout status:", kind, name))
	lines = append(lines, fmt.Sprintf("replicas: %d updated: %d ready: %d available: %d unavailable: %d",
		deploy.Status.Replicas,
		deploy.Status.UpdatedReplicas,
		deploy.Status.ReadyReplicas,
		deploy.Status.AvailableReplicas,
		deploy.Status.UnavailableReplicas,
	))
	for _, cond := range deploy.Status.Conditions {
		lines = append(lines, fmt.Sprintf("- %s: %s (%s) %s", cond.Type, cond.Status, cond.Reason, cond.Message))
	}
	return strings.Join(lines, "\n"), nil
}

func formatRolloutHistory(data []byte) (string, error) {
	var list struct {
		Items []struct {
			Metadata struct {
				Name string `json:"name"`
				Annotations map[string]string `json:"annotations"`
				CreationTimestamp time.Time `json:"creationTimestamp"`
			} `json:"metadata"`
		} `json:"items"`
	}
	if err := json.Unmarshal(data, &list); err != nil {
		return string(data), nil
	}
	var lines []string
	lines = append(lines, "REVISION\tNAME\tAGE")
	for _, rs := range list.Items {
		rev := rs.Metadata.Annotations["deployment.kubernetes.io/revision"]
		if rev == "" {
			rev = "-"
		}
		lines = append(lines, fmt.Sprintf("%s\t%s\t%s", rev, rs.Metadata.Name, formatAge(rs.Metadata.CreationTimestamp)))
	}
	return strings.Join(lines, "\n"), nil
}

func formatRevisionDetail(data []byte, revision int) (string, error) {
	var list struct {
		Items []struct {
			Metadata struct {
				Name        string            `json:"name"`
				Annotations map[string]string `json:"annotations"`
			} `json:"metadata"`
		} `json:"items"`
	}
	if err := json.Unmarshal(data, &list); err != nil {
		return string(data), nil
	}
	revStr := fmt.Sprintf("%d", revision)
	for _, rs := range list.Items {
		if rs.Metadata.Annotations["deployment.kubernetes.io/revision"] == revStr {
			encoded, err := json.MarshalIndent(rs, "", "  ")
			if err != nil {
				return rs.Metadata.Name, nil
			}
			return string(encoded), nil
		}
	}
	return fmt.Sprintf("revision %d not found", revision), nil
}

func formatMetricsTable(data []byte, resource string) (string, error) {
	if resource == "nodes" || resource == "node" {
		var list struct {
			Items []struct {
				Metadata struct {
					Name string `json:"name"`
				} `json:"metadata"`
				Usage struct {
					CPU    string `json:"cpu"`
					Memory string `json:"memory"`
				} `json:"usage"`
			} `json:"items"`
		}
		if err := json.Unmarshal(data, &list); err != nil {
			return string(data), nil
		}
		var lines []string
		lines = append(lines, "NAME\tCPU(cores)\tMEMORY(bytes)")
		for _, item := range list.Items {
			lines = append(lines, fmt.Sprintf("%s\t%s\t%s", item.Metadata.Name, item.Usage.CPU, item.Usage.Memory))
		}
		return strings.Join(lines, "\n"), nil
	}
	var list struct {
		Items []struct {
			Metadata struct {
				Name string `json:"name"`
			} `json:"metadata"`
			Containers []struct {
				Name  string `json:"name"`
				Usage struct {
					CPU    string `json:"cpu"`
					Memory string `json:"memory"`
				} `json:"usage"`
			} `json:"containers"`
		} `json:"items"`
	}
	if err := json.Unmarshal(data, &list); err != nil {
		return string(data), nil
	}
	var lines []string
	lines = append(lines, "POD\tCPU\tMEMORY")
	for _, item := range list.Items {
		cpu, mem := aggregateContainerUsage(item.Containers)
		lines = append(lines, fmt.Sprintf("%s\t%s\t%s", item.Metadata.Name, cpu, mem))
	}
	return strings.Join(lines, "\n"), nil
}

func aggregateContainerUsage(containers []struct {
	Name  string `json:"name"`
	Usage struct {
		CPU    string `json:"cpu"`
		Memory string `json:"memory"`
	} `json:"usage"`
}) (string, string) {
	if len(containers) == 0 {
		return "", ""
	}
	if len(containers) == 1 {
		return containers[0].Usage.CPU, containers[0].Usage.Memory
	}
	return "multi", "multi"
}

func formatDescribe(resourceType string, resourceData, eventsData []byte) string {
	var parts []string
	parts = append(parts, fmt.Sprintf("=== %s ===", resourceType))
	parts = append(parts, string(resourceData))
	if len(eventsData) > 0 {
		parts = append(parts, "=== Events ===")
		parts = append(parts, string(eventsData))
	}
	return strings.Join(parts, "\n")
}

func labelsToSelector(labels map[string]string) string {
	if len(labels) == 0 {
		return ""
	}
	parts := make([]string, 0, len(labels))
	for k, v := range labels {
		parts = append(parts, k+"="+v)
	}
	return strings.Join(parts, ",")
}

func normalizeWorkloadKind(kind string) string {
	switch strings.ToLower(strings.TrimSpace(kind)) {
	case "", "deployment", "deploy":
		return "deployment"
	case "statefulset", "sts":
		return "statefulset"
	case "daemonset", "ds":
		return "daemonset"
	default:
		return strings.ToLower(kind)
	}
}

func workloadAPIBase(kind string) string {
	return "/apis/apps/v1"
}

func workloadResourceName(kind string) string {
	switch kind {
	case "statefulset":
		return "statefulsets"
	case "daemonset":
		return "daemonsets"
	default:
		return "deployments"
	}
}

func buildGetPath(resource, namespace, name string, allNamespaces bool) (string, error) {
	resource = strings.ToLower(strings.TrimSpace(resource))
	meta, ok := resourceRegistry[resource]
	if !ok {
		return "", fmt.Errorf("unsupported resource type %q", resource)
	}
	var path string
	if meta.clusterScoped || allNamespaces {
		path = meta.apiRoot + "/" + meta.collection
	} else {
		if namespace == "" {
			return "", fmt.Errorf("namespace is required for resource %s", resource)
		}
		path = fmt.Sprintf("%s/namespaces/%s/%s", meta.apiRoot, url.PathEscape(namespace), meta.collection)
	}
	if name != "" {
		path += "/" + url.PathEscape(name)
	}
	return path, nil
}

type resourceMeta struct {
	apiRoot        string
	collection     string
	clusterScoped  bool
}

var resourceRegistry = map[string]resourceMeta{
	"pod":          {apiRoot: "/api/v1", collection: "pods"},
	"pods":         {apiRoot: "/api/v1", collection: "pods"},
	"deployment":   {apiRoot: "/apis/apps/v1", collection: "deployments"},
	"deployments":  {apiRoot: "/apis/apps/v1", collection: "deployments"},
	"service":      {apiRoot: "/api/v1", collection: "services"},
	"services":     {apiRoot: "/api/v1", collection: "services"},
	"ingress":      {apiRoot: "/apis/networking.k8s.io/v1", collection: "ingresses"},
	"ingresses":    {apiRoot: "/apis/networking.k8s.io/v1", collection: "ingresses"},
	"configmap":    {apiRoot: "/api/v1", collection: "configmaps"},
	"configmaps":   {apiRoot: "/api/v1", collection: "configmaps"},
	"hpa":          {apiRoot: "/apis/autoscaling/v2", collection: "horizontalpodautoscalers"},
	"job":          {apiRoot: "/apis/batch/v1", collection: "jobs"},
	"jobs":         {apiRoot: "/apis/batch/v1", collection: "jobs"},
	"cronjob":      {apiRoot: "/apis/batch/v1", collection: "cronjobs"},
	"cronjobs":     {apiRoot: "/apis/batch/v1", collection: "cronjobs"},
	"statefulset":  {apiRoot: "/apis/apps/v1", collection: "statefulsets"},
	"statefulsets": {apiRoot: "/apis/apps/v1", collection: "statefulsets"},
	"daemonset":    {apiRoot: "/apis/apps/v1", collection: "daemonsets"},
	"daemonsets":   {apiRoot: "/apis/apps/v1", collection: "daemonsets"},
	"node":         {apiRoot: "/api/v1", collection: "nodes", clusterScoped: true},
	"nodes":        {apiRoot: "/api/v1", collection: "nodes", clusterScoped: true},
	"namespace":    {apiRoot: "/api/v1", collection: "namespaces", clusterScoped: true},
	"namespaces":   {apiRoot: "/api/v1", collection: "namespaces", clusterScoped: true},
	"pvc":          {apiRoot: "/api/v1", collection: "persistentvolumeclaims"},
	"pv":           {apiRoot: "/api/v1", collection: "persistentvolumes", clusterScoped: true},
	"persistentvolumeclaims": {apiRoot: "/api/v1", collection: "persistentvolumeclaims"},
	"persistentvolumes":      {apiRoot: "/api/v1", collection: "persistentvolumes", clusterScoped: true},
}

func formatClusterContexts(data []byte, project, location string) (string, error) {
	var list struct {
		Clusters []struct {
			Name     string `json:"name"`
			Location string `json:"location"`
		} `json:"clusters"`
	}
	if err := json.Unmarshal(data, &list); err != nil {
		return string(data), nil
	}
	var lines []string
	for _, c := range list.Clusters {
		loc := c.Location
		if loc == "" {
			loc = location
		}
		lines = append(lines, fmt.Sprintf("gke_%s_%s_%s", project, loc, c.Name))
	}
	if len(lines) == 0 {
		return "no GKE clusters found for this project", nil
	}
	return strings.Join(lines, "\n"), nil
}
