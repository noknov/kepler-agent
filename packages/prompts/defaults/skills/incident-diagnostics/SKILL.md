---
name: incident-diagnostics
description: Use when triaging production incidents, organizing timelines across logs/metrics/deploys, or synthesizing an evidence board before presenting findings.
---

# Incident Diagnostics

Structure operational investigations using three artifacts in your working notes and final response:

## 1. Incident Brief

Establish the scope before querying large log or trace ranges:
- **Service / Component**: Affected deployment, service, job, or repository
- **Environment / Cluster**: Target environment, GKE cluster, or namespace
- **Symptom**: Observed error, alert, latency spike, or user-visible regression
- **Time Window**: Exact UTC/local window and correlation with recent deployments or config changes

## 2. Incident Timeline

Order verified events chronologically across Slack reports, CI/CD workflow runs, Kubernetes events, and observability traces/logs:
- `timestamp` | `source` | `event` | `impact / evidence`

## 3. Evidence Board

Separate verified observations from unverified hypotheses before concluding:
- **Verified Facts**: Concrete metrics, log lines, trace IDs, or commit SHAs with source references
- **Hypotheses**: Candidate root causes paired with confirming or disconfirming evidence
- **Operational Risks**: Blast radius, customer impact, or rollback considerations
- **Next Checks**: Immediate deterministic actions or verification steps
