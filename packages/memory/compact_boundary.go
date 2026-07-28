package memory

import "time"

type CompactBoundary struct {
	ID         string    `json:"id"`
	Layer      string    `json:"layer"`
	Summary    string    `json:"summary,omitempty"`
	PreTokens  int       `json:"pre_tokens,omitempty"`
	PostTokens int       `json:"post_tokens,omitempty"`
	CreatedAt  time.Time `json:"created_at"`
}
