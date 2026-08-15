-- PRAGMAs are set per-connection through the DSN (see db.go), not here.

CREATE TABLE users (
  id            INTEGER PRIMARY KEY,
  username      TEXT NOT NULL UNIQUE,
  password_hash TEXT NOT NULL,
  created_at    INTEGER NOT NULL
);

CREATE TABLE sessions (
  id         TEXT PRIMARY KEY,
  user_id    INTEGER NOT NULL REFERENCES users(id) ON DELETE CASCADE,
  expires_at INTEGER NOT NULL
);

CREATE INDEX idx_sessions_expires_at ON sessions(expires_at);

CREATE TABLE projects (
  id         INTEGER PRIMARY KEY,
  slug       TEXT NOT NULL UNIQUE,
  name       TEXT NOT NULL,
  created_at INTEGER NOT NULL
);

CREATE TABLE environments (
  id         INTEGER PRIMARY KEY,
  project_id INTEGER NOT NULL REFERENCES projects(id) ON DELETE CASCADE,
  slug       TEXT NOT NULL,
  updated_at INTEGER NOT NULL,
  created_at INTEGER NOT NULL,
  UNIQUE (project_id, slug)
);

CREATE TABLE api_keys (
  id             INTEGER PRIMARY KEY,
  environment_id INTEGER NOT NULL REFERENCES environments(id) ON DELETE CASCADE,
  key_hash       TEXT NOT NULL UNIQUE,
  key_prefix     TEXT NOT NULL,
  scope          TEXT NOT NULL CHECK (scope IN ('read','write','admin')),
  label          TEXT NOT NULL DEFAULT '',
  created_at     INTEGER NOT NULL,
  revoked_at     INTEGER
);

CREATE INDEX idx_api_keys_environment_id ON api_keys(environment_id);

CREATE TABLE feature_flags (
  id             INTEGER PRIMARY KEY,
  environment_id INTEGER NOT NULL REFERENCES environments(id) ON DELETE CASCADE,
  key            TEXT NOT NULL,
  enabled        INTEGER NOT NULL CHECK (enabled IN (0,1)),
  description    TEXT NOT NULL DEFAULT '',
  version        INTEGER NOT NULL DEFAULT 1,
  updated_at     INTEGER NOT NULL,
  UNIQUE (environment_id, key)
);

CREATE TABLE configs (
  id             INTEGER PRIMARY KEY,
  environment_id INTEGER NOT NULL REFERENCES environments(id) ON DELETE CASCADE,
  key            TEXT NOT NULL,
  value          TEXT NOT NULL,
  description    TEXT NOT NULL DEFAULT '',
  version        INTEGER NOT NULL DEFAULT 1,
  updated_at     INTEGER NOT NULL,
  UNIQUE (environment_id, key)
);
