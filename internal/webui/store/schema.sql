-- FeedWatch WebUI schema, version 6.
-- Every statement is IF NOT EXISTS, so running this on an older database only
-- adds the tables it is missing. Adding a COLUMN to an existing table is not
-- covered by this file: migrate.go carries those as explicit ALTER TABLE steps.
-- Executed inside migrate.go's transaction.
-- Timestamp columns are RFC3339 UTC strings so they sort lexicographically.

CREATE TABLE IF NOT EXISTS users (
  id                    INTEGER PRIMARY KEY AUTOINCREMENT,
  username              TEXT NOT NULL UNIQUE,
  password_hash         TEXT NOT NULL,
  role                  TEXT NOT NULL DEFAULT 'user' CHECK (role IN ('admin', 'user')),
  is_active             INTEGER NOT NULL DEFAULT 1,
  must_change_password  INTEGER NOT NULL DEFAULT 0,
  -- IANA zone name for display ("Asia/Shanghai"); empty means "use the
  -- server's zone". Purely cosmetic: no timestamp is ever stored per zone.
  timezone              TEXT NOT NULL DEFAULT '',
  -- Shape of this account's notifications: "digest" merges every new release of
  -- a cycle into one message, "per_feed" sends one message per subscribed
  -- project. Deliberately per ACCOUNT, not per subscription: a single cycle can
  -- produce both shapes, which is more messages than the user asked for and
  -- impossible to predict from the subscription list.
  notify_mode           TEXT NOT NULL DEFAULT 'digest' CHECK (notify_mode IN ('digest', 'per_feed')),
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

-- Channel credentials sealed with the master key (AES-256-GCM). Kept out of
-- channels.config_json so no ordinary channel query, export or debug dump ever
-- carries ciphertext; the row disappears with its channel.
CREATE TABLE IF NOT EXISTS channel_secrets (
  channel_id  INTEGER PRIMARY KEY REFERENCES channels(id) ON DELETE CASCADE,
  ciphertext  TEXT NOT NULL,
  updated_at  TEXT NOT NULL
);

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
-- The admin drill-down lists one user's recent deliveries newest first.
CREATE INDEX IF NOT EXISTS idx_deliveries_user ON deliveries(user_id, id);

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

-- ===== Subscription catalog (v5) =====
-- The catalog is a browsable set of TEMPLATES, not anybody's subscription.
-- "Subscribe" copies an entry into the user's own subscriptions row; disabling
-- or deleting an entry here never touches subscriptions, feed_pool or user_seen.

CREATE TABLE IF NOT EXISTS catalog_categories (
  id          INTEGER PRIMARY KEY AUTOINCREMENT,
  name        TEXT NOT NULL UNIQUE,
  created_at  TEXT NOT NULL,
  updated_at  TEXT NOT NULL
);

-- Only approved, browsable templates live here. NULL category_id means
-- "uncategorized": there is deliberately no sentinel row to rename or delete.
CREATE TABLE IF NOT EXISTS catalog_entries (
  id           INTEGER PRIMARY KEY AUTOINCREMENT,
  url          TEXT NOT NULL,
  url_key      TEXT NOT NULL,
  name         TEXT NOT NULL,
  category_id  INTEGER REFERENCES catalog_categories(id) ON DELETE SET NULL,
  description  TEXT NOT NULL DEFAULT '',
  homepage     TEXT NOT NULL DEFAULT '',
  source_type  TEXT NOT NULL DEFAULT 'atom',
  enabled      INTEGER NOT NULL DEFAULT 1,
  created_by   INTEGER REFERENCES users(id) ON DELETE SET NULL,
  created_at   TEXT NOT NULL,
  updated_at   TEXT NOT NULL
);
-- Disabled entries still claim their URL: re-enable instead of re-submitting.
CREATE UNIQUE INDEX IF NOT EXISTS uq_catalog_entries_url_key ON catalog_entries(url_key);
CREATE INDEX IF NOT EXISTS idx_catalog_entries_category ON catalog_entries(category_id);
CREATE INDEX IF NOT EXISTS idx_catalog_entries_enabled ON catalog_entries(enabled, id);

-- Every user submission (new entry or an update to an existing one). Rows are
-- review history: they are never deleted, and only approval writes to the
-- catalog. before_json snapshots the entry at submission time so a reviewer
-- can compare the old values with the proposed ones in this row.
CREATE TABLE IF NOT EXISTS submissions (
  id            INTEGER PRIMARY KEY AUTOINCREMENT,
  kind          TEXT NOT NULL CHECK (kind IN ('new', 'update')),
  -- "withdrawn" is a submission its own author pulled back before review;
  -- it is kept as history, like approved/rejected rows.
  status        TEXT NOT NULL DEFAULT 'pending'
                CHECK (status IN ('pending', 'approved', 'rejected', 'withdrawn')),
  entry_id      INTEGER REFERENCES catalog_entries(id) ON DELETE SET NULL,
  url           TEXT NOT NULL,
  url_key       TEXT NOT NULL,
  name          TEXT NOT NULL,
  category_id   INTEGER REFERENCES catalog_categories(id) ON DELETE SET NULL,
  description   TEXT NOT NULL DEFAULT '',
  homepage      TEXT NOT NULL DEFAULT '',
  before_json   TEXT NOT NULL DEFAULT '',
  submitted_by  INTEGER REFERENCES users(id) ON DELETE SET NULL,
  reject_reason TEXT NOT NULL DEFAULT '',
  reviewed_by   INTEGER REFERENCES users(id) ON DELETE SET NULL,
  reviewed_at   TEXT,
  created_at    TEXT NOT NULL,
  updated_at    TEXT NOT NULL
);
-- At most one pending proposal per URL, whatever its kind. Historical
-- approved/rejected rows may repeat a url_key.
CREATE UNIQUE INDEX IF NOT EXISTS uq_submissions_pending_url
  ON submissions(url_key) WHERE status = 'pending';
CREATE INDEX IF NOT EXISTS idx_submissions_status ON submissions(status, id);
CREATE INDEX IF NOT EXISTS idx_submissions_user ON submissions(submitted_by, id);
CREATE INDEX IF NOT EXISTS idx_submissions_entry ON submissions(entry_id);

-- ===== Remote preset state (v6) =====
-- The catalog_entries preset columns are NOT declared here: adding a column to
-- an existing table is an incremental ALTER in migrate.go, and on an upgraded
-- database this file runs BEFORE those columns exist. The partial unique index
-- on preset_key is created there for the same reason.
CREATE TABLE IF NOT EXISTS catalog_preset_state (
  preset_key   TEXT PRIMARY KEY,
  last_version INTEGER NOT NULL DEFAULT 0,
  deleted      INTEGER NOT NULL DEFAULT 0,
  updated_at   TEXT NOT NULL
);
