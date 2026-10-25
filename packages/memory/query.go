package memory

import (
	"context"
	"fmt"
	"strings"
	"time"
)

type Query struct {
	Embedding          []float64
	EmbeddingSpace     string
	Keys               []string
	Text               string
	Limit, Offset      int
	IncludePreferences bool
	ExcludePreferences bool
	Kind               string
	Mode               string // lexical (default), hybrid, semantic
	FusionK            int
}
type Page struct {
	Generation int64    `json:"generation"`
	Records    []Record `json:"records"`
	NextOffset *int     `json:"next_offset,omitempty"`
}

const MaxPageSize = 100

// Query ranks the entire authenticated namespace before paging. Hybrid uses
// reciprocal rank fusion of PostgreSQL full-text and Unicode trigram ranks.
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
	if len(query.Keys) > MaxPageSize {
		return page, fmt.Errorf("too many memory keys")
	}
	if query.Kind != "" && !validKind(query.Kind) {
		return page, fmt.Errorf("unsupported memory kind")
	}
	if query.Mode != "" && query.Mode != "lexical" && query.Mode != "hybrid" && query.Mode != "semantic" {
		return page, fmt.Errorf("unsupported memory retrieval mode")
	}
	if query.FusionK == 0 {
		query.FusionK = 60
	}
	if query.FusionK < 1 || query.FusionK > 10000 {
		return page, fmt.Errorf("invalid reciprocal rank fusion constant")
	}
	if query.Mode == "semantic" && query.Text != "" {
		if query.EmbeddingSpace == "" {
			return page, fmt.Errorf("semantic memory requires an embedding space")
		}
		vector, err := normalizedVector(query.Embedding)
		if err != nil {
			return page, err
		}
		query.Embedding = vector
	}
	query.Text = strings.ToLower(strings.TrimSpace(query.Text))
	eligible := `m.team_id=i.team_id AND m.user_id=i.user_id AND m.generation=COALESCE(g.generation,0)
 AND ($10::text[] IS NULL OR m.semantic_key=ANY($10::text[])) AND (m.expires_at IS NULL OR m.expires_at>NOW()) AND ($7='' OR m.kind=$7) AND (NOT $8 OR m.kind<>'preference')`
	selection := `SELECT m.* FROM user_memories m WHERE ` + eligible
	if query.Text != "" {
		if query.Mode == "hybrid" || query.Mode == "semantic" {
			semantic := `NULL::double precision`
			if query.Mode == "semantic" {
				semantic = `CASE WHEN g.embedding_space=$12 AND m.embedding_space=$12 AND m.embedding_version=m.version
 AND m.embedding_vector IS NOT NULL AND vector_dims(m.embedding_vector)=cardinality($11::double precision[])
 THEN -(m.embedding_vector <#> ($11::double precision[])::vector) END`
			}
			selection = `WITH scored AS (SELECT m.*,word_similarity($3,lower(m.semantic_key || ' ' || m.content)) AS lexical,
 ts_rank_cd(to_tsvector('simple',m.semantic_key || ' ' || m.content),websearch_to_tsquery('simple',$3)) AS fulltext,
 ` + semantic + ` AS semantic
 FROM user_memories m WHERE ` + eligible + `), ranked AS (
 SELECT *,CASE WHEN lexical>0 THEN row_number() OVER(ORDER BY lexical DESC,semantic_key) END AS lexical_rank,
 CASE WHEN fulltext>0 THEN row_number() OVER(ORDER BY fulltext DESC,semantic_key) END AS fulltext_rank,
 CASE WHEN semantic IS NOT NULL THEN row_number() OVER(ORDER BY semantic DESC NULLS LAST,semantic_key) END AS semantic_rank
 FROM scored WHERE lexical>0 OR fulltext>0 OR semantic IS NOT NULL OR ($4 AND kind='preference')
 ) SELECT * FROM ranked ORDER BY (lower(semantic_key)=$3) DESC,
 COALESCE(1.0/($9+lexical_rank),0)+COALESCE(1.0/($9+fulltext_rank),0)+COALESCE(1.0/($9+semantic_rank),0) DESC,updated_at DESC,semantic_key`
		} else {
			selection += ` AND (word_similarity($3,lower(m.semantic_key || ' ' || m.content))>0 OR ($4 AND m.kind='preference')) ORDER BY lower(m.semantic_key || ' ' || m.content) <->> $3,m.updated_at DESC,m.semantic_key`
		}
	} else {
		selection += ` ORDER BY m.updated_at DESC,m.semantic_key`
	}
	// $9 is referenced on every path so PostgreSQL can infer its type.
	sql := `SELECT COALESCE(g.generation,0),m.semantic_key,m.kind,m.content,m.source_session_id,m.source_turn_id,m.version,m.updated_at,m.expires_at
 FROM (SELECT $1::text AS team_id,$2::text AS user_id,$3::text AS query,$4::boolean AS include_preferences,$9::integer AS fusion_k,$11::double precision[] AS query_embedding,$12::text AS embedding_space) i
 LEFT JOIN user_memory_generations g USING(team_id,user_id)
 LEFT JOIN LATERAL (` + selection + ` LIMIT $5 OFFSET $6) m ON TRUE`
	rows, err := s.Pool.Query(ctx, sql, identity.TeamID, identity.UserID, query.Text, query.IncludePreferences, query.Limit+1, query.Offset, query.Kind, query.ExcludePreferences, query.FusionK, query.Keys, query.Embedding, query.EmbeddingSpace)
	if err != nil {
		return page, err
	}
	defer rows.Close()
	page.Records = []Record{}
	for rows.Next() {
		var key, kind, content, session, turn *string
		var version *int64
		var updated, expires *time.Time
		if err := rows.Scan(&page.Generation, &key, &kind, &content, &session, &turn, &version, &updated, &expires); err != nil {
			return page, err
		}
		if key != nil {
			page.Records = append(page.Records, Record{Key: *key, Kind: *kind, Content: *content, SessionID: *session, TurnID: *turn, Version: *version, UpdatedAt: *updated, ExpiresAt: expires})
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
