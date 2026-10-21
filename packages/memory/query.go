package memory

import (
	"context"
	"fmt"
	"strings"
	"time"
)

type Query struct {
	Text               string
	Limit              int
	Offset             int
	IncludePreferences bool
}

type Page struct {
	Generation int64    `json:"generation"`
	Records    []Record `json:"records"`
	NextOffset *int     `json:"next_offset,omitempty"`
}

const MaxPageSize = 100

// Query ranks across the complete authenticated namespace in PostgreSQL. pg_trgm
// supplies Unicode-aware character similarity without space-based token guesses,
// application keyword lists or a pre-truncated recent-record candidate pool.
func (s PGStore) Query(ctx context.Context, identity Identity, query Query) (Page, error) {
	var page Page
	if err := identity.Validate(); err != nil {
		return page, err
	}
	if s.Pool == nil {
		return page, fmt.Errorf("memory store unavailable")
	}
	if query.Limit == 0 {
		query.Limit = 20
	}
	if query.Limit < 1 || query.Limit > MaxPageSize || query.Offset < 0 {
		return page, fmt.Errorf("memory limit must be 1..%d and offset cannot be negative", MaxPageSize)
	}
	query.Text = strings.TrimSpace(query.Text)
	filter := ""
	order := "m.updated_at DESC,m.semantic_key"
	if query.Text != "" {
		// Zero overlap is excluded; ranking itself is PostgreSQL's standard word
		// similarity distance. Preferences are eligible even without a text match.
		filter = " AND (word_similarity($3,lower(m.semantic_key || ' ' || m.content))>0 OR ($4 AND m.kind='preference'))"
		order = "lower(m.semantic_key || ' ' || m.content) <->> $3,m.updated_at DESC,m.semantic_key"
	}
	sql := `SELECT COALESCE(g.generation,0),m.semantic_key,m.kind,m.content,m.source_session_id,m.source_turn_id,m.version,m.updated_at
 FROM (SELECT $1::text AS team_id,$2::text AS user_id,$3::text AS query,$4::boolean AS include_preferences) i
 LEFT JOIN user_memory_generations g USING(team_id,user_id)
 LEFT JOIN LATERAL (
 SELECT m.* FROM user_memories m WHERE m.team_id=i.team_id AND m.user_id=i.user_id AND m.generation=COALESCE(g.generation,0)` + filter + `
 ORDER BY ` + order + ` LIMIT $5 OFFSET $6
 ) m ON TRUE`
	rows, err := s.Pool.Query(ctx, sql, identity.TeamID, identity.UserID, query.Text, query.IncludePreferences, query.Limit+1, query.Offset)
	if err != nil {
		return page, err
	}
	defer rows.Close()
	page.Records = []Record{}
	for rows.Next() {
		var key, kind, content, session, turn *string
		var version *int64
		var updated *time.Time
		if err := rows.Scan(&page.Generation, &key, &kind, &content, &session, &turn, &version, &updated); err != nil {
			return page, err
		}
		if key != nil {
			page.Records = append(page.Records, Record{Key: *key, Kind: *kind, Content: *content, SessionID: *session, TurnID: *turn, Version: *version, UpdatedAt: *updated})
		}
	}
	if err := rows.Err(); err != nil {
		return page, err
	}
	if len(page.Records) > query.Limit {
		next := query.Offset + query.Limit
		page.NextOffset = &next
		page.Records = page.Records[:query.Limit]
	}
	return page, nil
}
