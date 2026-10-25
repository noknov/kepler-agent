package memory

import (
	"context"
	"crypto/rand"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/noknov/kepler-agent/packages/agent/model"
)

// pgvector exact vector storage supports up to 16,000 dimensions.
const MaxVectorDimensions = 16000

var ErrEmbeddingSpace = errors.New("memory embedding space differs; explicitly reindex before switching models")

// SemanticStore adds a derived, versioned cache to the existing fact owner.
// Only hybrid recall queries use it; lists, exact-key fences and preferences do
// not issue embedding requests. The ordinary store remains the write/delete API.
type SemanticStore struct {
	PGStore
	Client                model.Embedder
	Model, Space          string
	Dimensions, BatchSize int
	Timeout               time.Duration
	Degraded              func(error)
}

func (s SemanticStore) Validate() error {
	if s.Pool == nil || s.Client == nil || s.Model == "" || s.Space == "" || s.Dimensions < 1 || s.Dimensions > MaxVectorDimensions || s.BatchSize < 1 || s.BatchSize > MaxPageSize || s.Timeout <= 0 || s.Timeout > time.Duration(math.MaxInt64/2) {
		return fmt.Errorf("invalid semantic memory configuration")
	}
	return nil
}
func (s SemanticStore) Query(ctx context.Context, id Identity, query Query) (Page, error) {
	if query.Text == "" || query.Mode != "hybrid" {
		return s.PGStore.Query(ctx, id, query)
	}
	if err := s.Validate(); err != nil {
		return Page{}, err
	}
	generation, err := s.ensureSpace(ctx, id)
	if err != nil {
		return s.fallback(ctx, id, query, err)
	}
	if err = s.index(ctx, id, generation); err != nil {
		return s.fallback(ctx, id, query, err)
	}
	result, err := s.embed(ctx, []string{query.Text}, model.WorkInteractive)
	if err != nil {
		return s.fallback(ctx, id, query, err)
	}
	if len(result.Vectors) != 1 || len(result.Vectors[0]) != s.Dimensions {
		return s.fallback(ctx, id, query, fmt.Errorf("invalid query embedding dimensions"))
	}
	vector, err := normalizedVector(result.Vectors[0])
	if err != nil {
		return s.fallback(ctx, id, query, err)
	}
	query.Mode, query.EmbeddingSpace, query.Embedding = "semantic", s.Space, vector
	return s.PGStore.Query(ctx, id, query)
}
func (s SemanticStore) fallback(ctx context.Context, id Identity, q Query, cause error) (Page, error) {
	if ctx.Err() != nil {
		return Page{}, ctx.Err()
	}
	if s.Degraded != nil {
		s.Degraded(cause)
	}
	return s.PGStore.Query(ctx, id, q)
}
func (s SemanticStore) embed(ctx context.Context, input []string, class model.WorkClass) (model.EmbeddingResponse, error) {
	ctx, cancel := context.WithTimeout(ctx, s.Timeout)
	defer cancel()
	ctx = model.WithWorkload(ctx, model.Workload{Class: class})
	// Serialized UTF-8 bytes provide an explicit conservative estimate for this
	// adapter; only reported provider usage is treated as known consumption.
	encoded, _ := json.Marshal(input)
	return s.Client.Embed(ctx, model.EmbeddingRequest{Model: s.Model, Dimensions: s.Dimensions, Input: input, InputTokenEstimate: len(encoded), Metadata: map[string]string{"request_id": rand.Text()}})
}
func (s SemanticStore) ensureSpace(ctx context.Context, id Identity) (int64, error) {
	if err := id.Validate(); err != nil {
		return 0, err
	}
	var generation int64
	err := s.Pool.QueryRow(ctx, `SELECT COALESCE((SELECT generation FROM user_memory_generations WHERE team_id=$1 AND user_id=$2),0)`, id.TeamID, id.UserID).Scan(&generation)
	if err != nil {
		return 0, err
	}
	err = s.PGStore.mutate(ctx, id, generation, func(tx pgx.Tx) error {
		var space string
		if err := tx.QueryRow(ctx, `SELECT embedding_space FROM user_memory_generations WHERE team_id=$1 AND user_id=$2`, id.TeamID, id.UserID).Scan(&space); err != nil {
			return err
		}
		if space == s.Space {
			return nil
		}
		if space != "" {
			return ErrEmbeddingSpace
		}
		_, err := tx.Exec(ctx, `UPDATE user_memory_generations SET embedding_space=$3 WHERE team_id=$1 AND user_id=$2`, id.TeamID, id.UserID, s.Space)
		return err
	})
	return generation, err
}

// SwitchSpace is an explicit operator CAS, not a model tool. The expected
// space prevents a stale deployment from resetting a newer cache. Facts and
// their versions are preserved; old index owners cannot commit afterward.
func (s SemanticStore) SwitchSpace(ctx context.Context, id Identity, expected string) error {
	if err := s.Validate(); err != nil {
		return err
	}
	page, err := s.PGStore.Query(ctx, id, Query{Limit: 1})
	if err != nil {
		return err
	}
	return s.PGStore.mutate(ctx, id, page.Generation, func(tx pgx.Tx) error {
		tag, err := tx.Exec(ctx, `UPDATE user_memory_generations SET embedding_space=$3 WHERE team_id=$1 AND user_id=$2 AND embedding_space=$4`, id.TeamID, id.UserID, s.Space, expected)
		if err != nil {
			return err
		}
		if tag.RowsAffected() != 1 {
			return ErrEmbeddingSpace
		}
		_, err = tx.Exec(ctx, `UPDATE user_memories SET embedding_space='',embedding_version=0,embedding_vector=NULL,embedding_claim_owner='',embedding_claim_until=NULL WHERE team_id=$1 AND user_id=$2`, id.TeamID, id.UserID)
		return err
	})
}

func normalizedVector(input []float64) ([]float64, error) {
	if len(input) == 0 || len(input) > model.MaxEmbeddingDimensions {
		return nil, fmt.Errorf("invalid embedding dimensions")
	}
	scale := 0.0
	for _, value := range input {
		if math.IsNaN(value) || math.IsInf(value, 0) {
			return nil, fmt.Errorf("non-finite embedding")
		}
		scale = math.Max(scale, math.Abs(value))
	}
	if scale == 0 {
		return nil, fmt.Errorf("zero embedding vector")
	}
	sum := 0.0
	for _, value := range input {
		scaled := value / scale
		sum += scaled * scaled
	}
	norm := math.Sqrt(sum)
	out := make([]float64, len(input))
	for i, value := range input {
		out[i] = (value / scale) / norm
	}
	return out, nil
}
