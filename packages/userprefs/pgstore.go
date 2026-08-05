package userprefs

import (
	"context"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

type PGStore struct {
	pool *pgxpool.Pool
}

func NewPGStore(pool *pgxpool.Pool) *PGStore { return &PGStore{pool: pool} }

func (s *PGStore) GetSettings(ctx context.Context, userID string) (Settings, error) {
	var out Settings
	err := s.pool.QueryRow(ctx, `
INSERT INTO user_settings (user_id) VALUES ($1)
ON CONFLICT (user_id) DO UPDATE SET user_id=EXCLUDED.user_id
RETURNING user_id, web_search_enabled, updated_at`, userID).Scan(&out.UserID, &out.WebSearchEnabled, &out.UpdatedAt)
	return out, err
}

func (s *PGStore) SetWebSearchEnabled(ctx context.Context, userID string, enabled bool) error {
	_, err := s.pool.Exec(ctx, `
INSERT INTO user_settings (user_id, web_search_enabled, updated_at) VALUES ($1, $2, NOW())
ON CONFLICT (user_id) DO UPDATE SET web_search_enabled=EXCLUDED.web_search_enabled, updated_at=NOW()`, userID, enabled)
	return err
}

func (s *PGStore) ListAssets(ctx context.Context, userID string, kind AssetKind) ([]Asset, error) {
	rows, err := s.pool.Query(ctx, `
SELECT id, user_id, kind, name, description, content, source_file_id, active, created_at, updated_at
FROM user_prompt_assets
WHERE user_id=$1 AND kind=$2 AND active=TRUE
ORDER BY lower(name)`, userID, string(kind))
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Asset
	for rows.Next() {
		var asset Asset
		var kindText string
		if err := rows.Scan(&asset.ID, &asset.UserID, &kindText, &asset.Name, &asset.Description, &asset.Content, &asset.SourceFileID, &asset.Active, &asset.CreatedAt, &asset.UpdatedAt); err != nil {
			return nil, err
		}
		asset.Kind = AssetKind(kindText)
		out = append(out, asset)
	}
	return out, rows.Err()
}

func (s *PGStore) UpsertAsset(ctx context.Context, asset Asset) (Asset, error) {
	asset = normalizeAsset(asset)
	if err := validateAsset(asset); err != nil {
		return Asset{}, err
	}
	now := time.Now().UTC()
	var kindText string
	err := s.pool.QueryRow(ctx, `
INSERT INTO user_prompt_assets
 (id, user_id, kind, name, description, content, source_file_id, active, created_at, updated_at)
VALUES ($1,$2,$3,$4,$5,$6,$7,TRUE,$8,$8)
ON CONFLICT (id) DO UPDATE SET
 description=EXCLUDED.description,
 content=EXCLUDED.content,
 source_file_id=EXCLUDED.source_file_id,
 active=TRUE,
 updated_at=EXCLUDED.updated_at
RETURNING id, user_id, kind, name, description, content, source_file_id, active, created_at, updated_at`,
		asset.ID, asset.UserID, string(asset.Kind), asset.Name, asset.Description, asset.Content, asset.SourceFileID, now,
	).Scan(&asset.ID, &asset.UserID, &kindText, &asset.Name, &asset.Description, &asset.Content, &asset.SourceFileID, &asset.Active, &asset.CreatedAt, &asset.UpdatedAt)
	asset.Kind = AssetKind(kindText)
	return asset, err
}

func (s *PGStore) DeleteAssets(ctx context.Context, userID string, kind AssetKind) error {
	_, err := s.pool.Exec(ctx, `
UPDATE user_prompt_assets SET active=FALSE, updated_at=NOW()
WHERE user_id=$1 AND kind=$2`, userID, string(kind))
	return err
}

func (s *PGStore) DeleteAsset(ctx context.Context, userID string, kind AssetKind, id string) error {
	_, err := s.pool.Exec(ctx, `
UPDATE user_prompt_assets SET active=FALSE, updated_at=NOW()
WHERE user_id=$1 AND kind=$2 AND id=$3`, userID, string(kind), id)
	return err
}
