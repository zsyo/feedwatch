-- FeedWatch WebUI schema, version 1.
-- All tables are created up front: later feature points only add read/write
-- helpers, never a second migration. Executed inside migrate.go's transaction.
-- Timestamp columns are RFC3339 UTC strings so they sort lexicographically.

CREATE TABLE IF NOT EXISTS users (
  id                    INTEGER PRIMARY KEY AUTOINCREMENT,
  username              TEXT NOT NULL UNIQUE,
  password_hash         TEXT NOT NULL,
  role                  TEXT NOT NULL DEFAULT 'user' CHECK (role IN ('admin', 'user')),
  is_active             INTEGER NOT NULL DEFAULT 1,
  must_change_password  INTEGER NOT NULL DEFAULT 0,
  created_at            TEXT NOT NULL,
  updated_at            TEXT NOT NULL
);

CREATE TABLE IF NOT EXISTS sessions (
  token_hash    TEXT PRIMARY KEY,
  user_id       INTEGER NOT NULL REFERENCES users(id) ON DELETE CASCADE,
  csrf_token    TEXT NOT NULL,
  expires_at    TEXT NOT NULL,
  last_seen_at  TEXT NOT NULL
);
CREATE INDEX IF NOT EXISTS idx_sessions_expires ON sessions(expires_at);

CREATE TABLE IF NOT EXISTS subscriptions (
  id             INTEGER PRIMARY KEY AUTOINCREMENT,
  user_id        INTEGER NOT NULL REFERENCES users(id) ON DELETE CASCADE,
  url            TEXT NOT NULL,
  url_key        TEXT NOT NULL,
  display_name   TEXT NOT NULL DEFAULT '',
  source_type    TEXT NOT NULL DEFAULT 'atom',
  filter_json    TEXT NOT NULL DEFAULT '{}',
  notify_policy  TEXT NOT NULL DEFAULT 'on_update' CHECK (notify_policy IN ('always', 'on_update')),
  mode           TEXT NOT NULL DEFAULT 'digest' CHECK (mode IN ('digest', 'per_feed')),
  enabled        INTEGER NOT NULL DEFAULT 1,
  seeded_at      TEXT,
  deleted_at     TEXT
);
-- A user may re-subscribe after deleting; only one live row per (user, URL).
CREATE UNIQUE INDEX IF NOT EXISTS uq_subscriptions_live
  ON subscriptions(user_id, url_key) WHERE deleted_at IS NULL;
CREATE INDEX IF NOT EXISTS idx_subscriptions_url_key ON subscriptions(url_key);

CREATE TABLE IF NOT EXISTS channels (
  id           INTEGER PRIMARY KEY AUTOINCREMENT,
  user_id      INTEGER NOT NULL REFERENCES users(id) ON DELETE CASCADE,
  type         TEXT NOT NULL,
  name         TEXT NOT NULL DEFAULT '',
  config_json  TEXT NOT NULL DEFAULT '{}',
  enabled      INTEGER NOT NULL DEFAULT 1
);
CREATE INDEX IF NOT EXISTS idx_channels_user ON channels(user_id);

-- Global poll pool: one row per normalized URL, fetched once per cycle no
-- matter how many users subscribe to it.
CREATE TABLE IF NOT EXISTS feed_pool (
  url_key        TEXT PRIMARY KEY,
  url            TEXT NOT NULL UNIQUE,
  source_type    TEXT NOT NULL DEFAULT 'atom',
  etag           TEXT NOT NULL DEFAULT '',
  last_modified  TEXT NOT NULL DEFAULT '',
  last_checked   TEXT,
  last_version   TEXT NOT NULL DEFAULT ''
);

CREATE TABLE IF NOT EXISTS seen_ids (
  url_key     TEXT NOT NULL,
  release_id  TEXT NOT NULL,
  seen_at     TEXT NOT NULL,
  PRIMARY KEY (url_key, release_id)
);

-- Per-user baseline: a new subscription copies the current global seen_ids, so
-- it never replays releases published before the user joined.
CREATE TABLE IF NOT EXISTS user_seen (
  user_id     INTEGER NOT NULL REFERENCES users(id) ON DELETE CASCADE,
  url_key     TEXT NOT NULL,
  release_id  TEXT NOT NULL,
  seen_at     TEXT NOT NULL,
  PRIMARY KEY (user_id, url_key, release_id)
);

-- Materialized, already rendered notifications. Delivery retries read these
-- rows instead of re-fetching the feed.
CREATE TABLE IF NOT EXISTS notifications (
  id          INTEGER PRIMARY KEY AUTOINCREMENT,
  cycle_id    TEXT NOT NULL,
  merge_key   TEXT NOT NULL DEFAULT '',
  kind        TEXT NOT NULL,
  subject     TEXT NOT NULL DEFAULT '',
  "text"      TEXT NOT NULL DEFAULT '',
  html        TEXT NOT NULL DEFAULT '',
  markdown    TEXT NOT NULL DEFAULT '',
  created_at  TEXT NOT NULL
);
CREATE INDEX IF NOT EXISTS idx_notifications_cycle ON notifications(cycle_id);

-- One network call per merged group.
CREATE TABLE IF NOT EXISTS delivery_groups (
  group_id          TEXT PRIMARY KEY,
  cycle_id          TEXT NOT NULL,
  merge_key         TEXT NOT NULL,
  channel_type      TEXT NOT NULL,
  idempotency_key   TEXT NOT NULL,
  status            TEXT NOT NULL DEFAULT 'pending',
  attempts          INTEGER NOT NULL DEFAULT 0,
  last_error        TEXT NOT NULL DEFAULT '',
  created_at        TEXT NOT NULL
);
CREATE INDEX IF NOT EXISTS idx_delivery_groups_status ON delivery_groups(status);

CREATE TABLE IF NOT EXISTS deliveries (
  id           INTEGER PRIMARY KEY AUTOINCREMENT,
  cycle_id     TEXT NOT NULL,
  group_id     TEXT NOT NULL,
  user_id      INTEGER NOT NULL REFERENCES users(id) ON DELETE CASCADE,
  channel_id   INTEGER NOT NULL,
  kind         TEXT NOT NULL,
  status       TEXT NOT NULL DEFAULT 'pending',
  attempts     INTEGER NOT NULL DEFAULT 0,
  sent_at      TEXT,
  UNIQUE (cycle_id, user_id, channel_id, group_id, kind)
);
CREATE INDEX IF NOT EXISTS idx_deliveries_status ON deliveries(status);

CREATE TABLE IF NOT EXISTS cycles (
  id           TEXT PRIMARY KEY,
  started_at   TEXT NOT NULL,
  finished_at  TEXT,
  urls         INTEGER NOT NULL DEFAULT 0,
  sent         INTEGER NOT NULL DEFAULT 0,
  failed       INTEGER NOT NULL DEFAULT 0
);

CREATE TABLE IF NOT EXISTS settings (
  key         TEXT PRIMARY KEY,
  value       TEXT NOT NULL,
  updated_at  TEXT NOT NULL
);

CREATE TABLE IF NOT EXISTS audit_log (
  id           INTEGER PRIMARY KEY AUTOINCREMENT,
  ts           TEXT NOT NULL,
  actor_id     INTEGER,
  action       TEXT NOT NULL,
  target       TEXT NOT NULL DEFAULT '',
  detail_json  TEXT NOT NULL DEFAULT '{}',
  ip           TEXT NOT NULL DEFAULT ''
);
CREATE INDEX IF NOT EXISTS idx_audit_ts ON audit_log(ts);
