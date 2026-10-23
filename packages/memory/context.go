package memory

import "time"

// ContextRecord is the disclosure contract for recall/ranking. Source session
// and turn identifiers remain in the authenticated store/audit interface; they
// are unnecessary for relevance decisions and are not sent to the model.
type ContextRecord struct {
	Key       string     `json:"semantic_key"`
	Kind      string     `json:"kind"`
	Content   string     `json:"content"`
	Version   int64      `json:"version"`
	UpdatedAt time.Time  `json:"updated_at"`
	ExpiresAt *time.Time `json:"expires_at,omitempty"`
}

func ForContext(records []Record) []ContextRecord {
	out := make([]ContextRecord, 0, len(records))
	for _, record := range records {
		out = append(out, ContextRecord{record.Key, record.Kind, record.Content, record.Version, record.UpdatedAt, record.ExpiresAt})
	}
	return out
}
