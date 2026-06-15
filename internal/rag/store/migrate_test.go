package store

import (
	"strings"
	"testing"
)

func TestMigrationSQL(t *testing.T) {
	sql := migrationSQL(1536)
	if !strings.Contains(sql, "vector(1536)") {
		t.Error("migration SQL should contain vector dimension")
	}
	if !strings.Contains(sql, "rag_chunks") {
		t.Error("migration SQL should create rag_chunks table")
	}
	if !strings.Contains(sql, "rag_index_state") {
		t.Error("migration SQL should create rag_index_state table")
	}
	if !strings.Contains(sql, "hnsw") {
		t.Error("migration SQL should create HNSW index")
	}
	if !strings.Contains(sql, "gin(tsv)") {
		t.Error("migration SQL should create GIN index for tsvector")
	}
	if !strings.Contains(sql, "left(content, 200000)") {
		t.Error("migration SQL should cap content used for tsvector")
	}
	if strings.Contains(sql, "ALTER TABLE rag_chunks DROP COLUMN tsv") {
		t.Error("startup migration should not rewrite existing rag_chunks tsv column")
	}
}

func TestToTSQuery(t *testing.T) {
	tests := []struct {
		input string
		want  string
	}{
		{"hello world", "hello:* & world:*"},
		{"auth", "auth:*"},
		{"", ""},
		{"foo-bar", "foobar:*"},
		{"user_id", "user_id:*"},
	}
	for _, tt := range tests {
		got := toTSQuery(tt.input)
		if got != tt.want {
			t.Errorf("toTSQuery(%q) = %q, want %q", tt.input, got, tt.want)
		}
	}
}
