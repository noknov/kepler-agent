package memory

import (
	"context"
	"crypto/rand"
	"fmt"

	"github.com/noknov/kepler-agent/packages/agent/model"
)

type indexRecord struct {
	Key, Content        string
	Generation, Version int64
}

// index claims bounded derived work in one short statement; no SQL connection
// or transaction is held while the embedding provider runs.
func (s SemanticStore) index(ctx context.Context, id Identity, generation int64) error {
	owner := rand.Text()
	rows, err := s.Pool.Query(ctx, `WITH candidates AS (
 SELECT m.team_id,m.user_id,m.semantic_key FROM user_memories m
 JOIN user_memory_generations g USING(team_id,user_id)
 WHERE m.team_id=$1 AND m.user_id=$2 AND m.generation=$3 AND g.generation=$3 AND g.embedding_space=$4
 AND m.kind<>'preference' AND (m.expires_at IS NULL OR m.expires_at>NOW())
 AND (m.embedding_vector IS NULL OR m.embedding_version<>m.version OR m.embedding_space<>$4)
 AND (m.embedding_claim_until IS NULL OR m.embedding_claim_until<=NOW() OR m.embedding_version<>m.version)
 ORDER BY m.updated_at,m.semantic_key LIMIT $5 FOR UPDATE OF m SKIP LOCKED
) UPDATE user_memories m SET embedding_vector=NULL,embedding_version=m.version,embedding_space=$4,
 embedding_claim_owner=$6,embedding_claim_until=NOW()+$7::bigint*INTERVAL '1 millisecond'
 FROM candidates c WHERE m.team_id=c.team_id AND m.user_id=c.user_id AND m.semantic_key=c.semantic_key
 RETURNING m.semantic_key,m.content,m.generation,m.version`, id.TeamID, id.UserID, generation, s.Space, s.BatchSize, owner, (2 * s.Timeout).Milliseconds())
	if err != nil {
		return err
	}
	var records []indexRecord
	for rows.Next() {
		var record indexRecord
		if err = rows.Scan(&record.Key, &record.Content, &record.Generation, &record.Version); err != nil {
			rows.Close()
			return err
		}
		records = append(records, record)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return err
	}
	if len(records) == 0 {
		return nil
	}
	defer func() {
		cleanup, cancel := context.WithTimeout(context.WithoutCancel(ctx), s.Timeout)
		defer cancel()
		// Release only our remaining claims. Completed rows and replacement owners
		// cannot be affected even after cancellation, reset or a model-space switch.
		_, _ = s.Pool.Exec(cleanup, `UPDATE user_memories SET embedding_claim_owner='',embedding_claim_until=NULL WHERE team_id=$1 AND user_id=$2 AND embedding_claim_owner=$3`, id.TeamID, id.UserID, owner)
	}()
	input := make([]string, len(records))
	for i, record := range records {
		input[i] = record.Key + "\n" + record.Content
	}
	response, err := s.embed(ctx, input, model.WorkBackground)
	if err != nil {
		return err
	}
	if len(response.Vectors) != len(records) {
		return fmt.Errorf("embedding batch response count differs")
	}
	vectors := make([][]float64, len(records))
	for i := range records {
		if len(response.Vectors[i]) != s.Dimensions {
			return fmt.Errorf("embedding cache dimension differs")
		}
		vector, err := normalizedVector(response.Vectors[i])
		if err != nil {
			return err
		}
		vectors[i] = vector
	}
	for i, record := range records {
		_, err = s.Pool.Exec(ctx, `UPDATE user_memories m SET embedding_vector=($9::double precision[])::vector,embedding_claim_owner='',embedding_claim_until=NULL
 WHERE m.team_id=$1 AND m.user_id=$2 AND m.semantic_key=$3 AND m.generation=$4 AND m.version=$5
 AND m.embedding_version=$5 AND m.embedding_space=$6 AND m.embedding_claim_owner=$7 AND m.embedding_claim_until>NOW()
 AND (m.expires_at IS NULL OR m.expires_at>NOW()) AND EXISTS(
 SELECT 1 FROM user_memory_generations g WHERE g.team_id=$1 AND g.user_id=$2 AND g.generation=$4 AND g.embedding_space=$6)
 AND cardinality($9::double precision[])=$8`, id.TeamID, id.UserID, record.Key, record.Generation, record.Version, s.Space, owner, s.Dimensions, vectors[i])
		if err != nil {
			return err
		}
	}
	return nil
}
