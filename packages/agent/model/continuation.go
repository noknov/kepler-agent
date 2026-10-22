package model

import "encoding/json"

// Continuation carries an indivisible provider window. Payloads are opaque to
// the runtime and must never be exported to telemetry or another provider.
// The accompanying text checkpoint is the portable fallback.
type Continuation struct {
	Version       int             `json:"version"`
	Provider      string          `json:"provider"`
	Model         string          `json:"model"`
	Route         string          `json:"route"`
	Kind          string          `json:"kind"` // response or compaction
	Items         json.RawMessage `json:"items"`
	TokenEstimate int             `json:"token_estimate"`
}

const OperationCompact = "compact"
