-- Tracewell schema (SQLite).
-- Statements are idempotent: applying this file twice is a no-op.

CREATE TABLE IF NOT EXISTS projects (
    id         INTEGER PRIMARY KEY,
    name       TEXT    NOT NULL UNIQUE,
    created_at TEXT    NOT NULL,
    updated_at TEXT    NOT NULL
);

CREATE TABLE IF NOT EXISTS traces (
    id            INTEGER PRIMARY KEY,
    project_rowid INTEGER NOT NULL REFERENCES projects (id) ON DELETE CASCADE,
    trace_id      TEXT    NOT NULL UNIQUE,
    session_id    TEXT,
    start_time    TEXT    NOT NULL,
    end_time      TEXT    NOT NULL
);

CREATE INDEX IF NOT EXISTS ix_traces_project_rowid_start_time
    ON traces (project_rowid, start_time DESC);

CREATE TABLE IF NOT EXISTS spans (
    id                                      INTEGER PRIMARY KEY,
    trace_rowid                             INTEGER NOT NULL REFERENCES traces (id) ON DELETE CASCADE,
    span_id                                 TEXT    NOT NULL UNIQUE,
    parent_id                               TEXT,
    name                                    TEXT    NOT NULL,
    span_kind                               TEXT    NOT NULL,
    start_time                              TEXT    NOT NULL,
    end_time                                TEXT    NOT NULL,
    attributes                              TEXT    NOT NULL,  -- JSON
    events                                  TEXT    NOT NULL,  -- JSON
    status_code                             TEXT    NOT NULL DEFAULT 'UNSET'
        CHECK (status_code IN ('OK', 'ERROR', 'UNSET')),
    status_message                          TEXT    NOT NULL,
    -- Cumulative values include this span and all of its descendants.
    -- Precomputed at write time so queries stay flat.
    cumulative_error_count                  INTEGER NOT NULL,
    cumulative_llm_token_count_prompt       INTEGER NOT NULL,
    cumulative_llm_token_count_completion   INTEGER NOT NULL,
    -- Self values (NULL unless this is an LLM span).
    llm_token_count_prompt                  INTEGER,
    llm_token_count_completion              INTEGER
);

CREATE INDEX IF NOT EXISTS ix_spans_trace_rowid ON spans (trace_rowid);
CREATE INDEX IF NOT EXISTS ix_spans_parent_id ON spans (parent_id);
CREATE INDEX IF NOT EXISTS ix_spans_start_time ON spans (start_time);
