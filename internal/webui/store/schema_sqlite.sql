-- FeedWatch WebUI schema for SQLite, version 8.
-- Executed statement by statement by migrate.go; indexes are created there too
-- (MySQL has no CREATE INDEX IF NOT EXISTS, so they cannot live in this file).
-- Timestamp columns are RFC3339 UTC strings so they sort lexicographically.
-- The MySQL and PostgreSQL twins must declare the same tables and columns;
-- TestSchemaFilesAgree enforces that.

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

CREATE TABLE IF NOT EXISTS channels (
  id           INTEGER PRIMARY KEY AUTOINCREMENT,
  user_id      INTEGER NOT NULL REFERENCES users(id) ON DELETE CASCADE,
  type         TEXT NOT NULL,
  name         TEXT NOT NULL DEFAULT '',
  config_json  TEXT NOT NULL DEFAULT '{}',
  enabled      INTEGER NOT NULL DEFAULT 1
);

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
-- rows instead of re-fetching the feed. The body column is called "body" and
-- not "text" because TEXT is a type name in MySQL and would need quoting there.
CREATE TABLE IF NOT EXISTS notifications (
  id          INTEGER PRIMARY KEY AUTOINCREMENT,
  cycle_id    TEXT NOT NULL,
  merge_key   TEXT NOT NULL DEFAULT '',
  kind        TEXT NOT NULL,
  subject     TEXT NOT NULL DEFAULT '',
  body        TEXT NOT NULL DEFAULT '',
  html        TEXT NOT NULL DEFAULT '',
  markdown    TEXT NOT NULL DEFAULT '',
  created_at  TEXT NOT NULL
);

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

CREATE TABLE IF NOT EXISTS cycles (
  id           TEXT PRIMARY KEY,
  started_at   TEXT NOT NULL,
  finished_at  TEXT,
  urls         INTEGER NOT NULL DEFAULT 0,
  sent         INTEGER NOT NULL DEFAULT 0,
  failed       INTEGER NOT NULL DEFAULT 0
);

-- "setting_key" and not "key": KEY is reserved in MySQL, and a column named
-- "key" would need backticks, which SQLite and PostgreSQL do not share.
CREATE TABLE IF NOT EXISTS settings (
  setting_key  TEXT PRIMARY KEY,
  value        TEXT NOT NULL,
  updated_at   TEXT NOT NULL
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
  updated_at   TEXT NOT NULL,
  preset_key     TEXT NOT NULL DEFAULT '',
  preset_version INTEGER NOT NULL DEFAULT 0,
  preset_edited  INTEGER NOT NULL DEFAULT 0
);

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

-- ===== Remote preset state (v6) =====
CREATE TABLE IF NOT EXISTS catalog_preset_state (
  preset_key   TEXT PRIMARY KEY,
  last_version INTEGER NOT NULL DEFAULT 0,
  deleted      INTEGER NOT NULL DEFAULT 0,
  updated_at   TEXT NOT NULL
);

-- ===== Third-party sign-in (v8) =====
-- One row per linked account. The provider's numeric user id is the identity:
-- a login name can be renamed upstream, the id cannot. Deleting the user takes
-- the binding with it.
CREATE TABLE IF NOT EXISTS user_identities (
  id         INTEGER PRIMARY KEY AUTOINCREMENT,
  user_id    INTEGER NOT NULL REFERENCES users(id) ON DELETE CASCADE,
  provider   TEXT NOT NULL,
  subject    TEXT NOT NULL,
  login      TEXT NOT NULL DEFAULT '',
  email      TEXT NOT NULL DEFAULT '',
  created_at TEXT NOT NULL,
  updated_at TEXT NOT NULL,
  -- One binding per provider account, and at most one per provider per user.
  UNIQUE (provider, subject),
  UNIQUE (provider, user_id)
);

-- The OAuth app credentials, one row per provider. client_secret holds a
-- sealed envelope (fwsec:v1:) and is never returned to the browser. A row
-- exists as soon as the provider is configured; enabled=0 keeps the buttons
-- off the login page without losing the credentials.
CREATE TABLE IF NOT EXISTS auth_providers (
  provider      TEXT PRIMARY KEY,
  enabled       INTEGER NOT NULL DEFAULT 0,
  client_id     TEXT NOT NULL DEFAULT '',
  client_secret TEXT NOT NULL DEFAULT '',
  updated_at    TEXT NOT NULL
);

-- A sign-in attempt in flight. The state is the bearer token in the callback
-- URL, so only its hash is stored. mode='bind' carries the user id that started
-- the flow; mode='login' has no user yet. Rows are single-use and expire.
CREATE TABLE IF NOT EXISTS oauth_flows (
  state_hash    TEXT PRIMARY KEY,
  provider      TEXT NOT NULL,
  mode          TEXT NOT NULL DEFAULT 'login',
  user_id       INTEGER REFERENCES users(id) ON DELETE CASCADE,
  redirect      TEXT NOT NULL DEFAULT '',
  code_verifier TEXT NOT NULL DEFAULT '',
  created_at    TEXT NOT NULL,
  expires_at    TEXT NOT NULL
);
