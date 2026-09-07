package store

import "fmt"

const structuredSchema = `
CREATE TABLE IF NOT EXISTS memory_entries (
    id           TEXT PRIMARY KEY,
    content      TEXT NOT NULL,
    scope        TEXT NOT NULL CHECK (scope IN ('session','user')),
    session_id   TEXT,
    source       TEXT NOT NULL,
    source_type  TEXT NOT NULL CHECK (source_type IN ('harness','etl')),
    external_id  TEXT,
    created_at   TEXT NOT NULL,
    updated_at   TEXT NOT NULL
);

CREATE INDEX IF NOT EXISTS idx_memory_entries_scope_session
    ON memory_entries(scope, session_id);

CREATE TABLE IF NOT EXISTS memory_tags (
    memory_id TEXT NOT NULL REFERENCES memory_entries(id) ON DELETE CASCADE,
    tag       TEXT NOT NULL,
    PRIMARY KEY (memory_id, tag)
);

CREATE INDEX IF NOT EXISTS idx_memory_tags_tag ON memory_tags(tag);

CREATE UNIQUE INDEX IF NOT EXISTS idx_memory_entries_source_extid_scope
    ON memory_entries(source, external_id, scope)
    WHERE external_id IS NOT NULL;
`

func vectorSchema(dim int) string {
	return fmt.Sprintf(
		`CREATE VIRTUAL TABLE IF NOT EXISTS memory_vectors USING vec0(embedding float[%d]);`,
		dim,
	)
}
