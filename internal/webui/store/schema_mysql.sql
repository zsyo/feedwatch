-- FeedWatch WebUI schema for MySQL 8.0.13+, version 9.
-- MySQL-only deviations from the SQLite twin, all deliberate:
--   * TIMESTAMP-ish, URL and key columns are VARCHAR/CHAR, because InnoDB
--     cannot index TEXT without a prefix length and TEXT cannot take a
--     literal DEFAULT before 8.0.13 (hence the DEFAULT ('') form).
--   * Unicode collation is utf8mb4_bin so string comparison and uniqueness
--     behave byte-wise, like SQLite, instead of case-insensitively.
--   * MySQL has no partial indexes, so the three "live row" unique indexes are
--     enforced through STORED generated columns that are NULL for rows the
--     index must ignore (MySQL treats NULLs as distinct).
--   * feed_pool.url is not UNIQUE here: a 2 KB URL cannot be uniquely indexed
--     under utf8mb4. url_key (its sha256) is the primary key and the real
--     identity, so the constraint would be redundant anyway.
-- Indexes are created by migrate.go, not here: CREATE INDEX has no IF NOT
-- EXISTS in MySQL. TestSchemaFilesAgree enforces table/column parity.

CREATE TABLE IF NOT EXISTS users (
  id                    INTEGER NOT NULL AUTO_INCREMENT PRIMARY KEY,
  username              VARCHAR(191) NOT NULL UNIQUE,
  password_hash         VARCHAR(255) NOT NULL,
  role                  VARCHAR(32) NOT NULL DEFAULT 'user',
  is_active             INTEGER NOT NULL DEFAULT 1,
  must_change_password  INTEGER NOT NULL DEFAULT 0,
  timezone              VARCHAR(64) NOT NULL DEFAULT '',
  notify_mode           VARCHAR(32) NOT NULL DEFAULT 'digest',
  baseline_on_subscribe INTEGER NOT NULL DEFAULT 1,
  created_at            VARCHAR(40) NOT NULL,
  updated_at            VARCHAR(40) NOT NULL,
  CONSTRAINT ck_users_role CHECK (role IN ('admin', 'user')),
  CONSTRAINT ck_users_notify_mode CHECK (notify_mode IN ('digest', 'per_feed'))
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_bin;

CREATE TABLE IF NOT EXISTS sessions (
  token_hash    CHAR(64) NOT NULL PRIMARY KEY,
  user_id       INTEGER NOT NULL,
  csrf_token    VARCHAR(64) NOT NULL,
  expires_at    VARCHAR(40) NOT NULL,
  last_seen_at  VARCHAR(40) NOT NULL,
  CONSTRAINT fk_sessions_user FOREIGN KEY (user_id) REFERENCES users(id) ON DELETE CASCADE
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_bin;

CREATE TABLE IF NOT EXISTS subscriptions (
  id             INTEGER NOT NULL AUTO_INCREMENT PRIMARY KEY,
  user_id        INTEGER NOT NULL,
  url            VARCHAR(1024) NOT NULL,
  url_key        CHAR(64) NOT NULL,
  display_name   VARCHAR(255) NOT NULL DEFAULT '',
  source_type    VARCHAR(32) NOT NULL DEFAULT 'atom',
  filter_json    MEDIUMTEXT NOT NULL DEFAULT (''),
  notify_policy  VARCHAR(32) NOT NULL DEFAULT 'on_update',
  enabled        INTEGER NOT NULL DEFAULT 1,
  seeded_at      VARCHAR(40) NULL,
  deleted_at     VARCHAR(40) NULL,
  -- Non-NULL only while the subscription is live; uq_subscriptions_live is
  -- UNIQUE(user_id, live_url_key), reproducing "one live row per URL".
  live_url_key   CHAR(64) GENERATED ALWAYS AS
                 (CASE WHEN deleted_at IS NULL THEN url_key ELSE NULL END) STORED,
  CONSTRAINT ck_subscriptions_policy CHECK (notify_policy IN ('always', 'on_update')),
  CONSTRAINT fk_subscriptions_user FOREIGN KEY (user_id) REFERENCES users(id) ON DELETE CASCADE
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_bin;

CREATE TABLE IF NOT EXISTS channels (
  id           INTEGER NOT NULL AUTO_INCREMENT PRIMARY KEY,
  user_id      INTEGER NOT NULL,
  type         VARCHAR(32) NOT NULL,
  name         VARCHAR(255) NOT NULL DEFAULT '',
  config_json  MEDIUMTEXT NOT NULL DEFAULT (''),
  enabled      INTEGER NOT NULL DEFAULT 1,
  CONSTRAINT fk_channels_user FOREIGN KEY (user_id) REFERENCES users(id) ON DELETE CASCADE
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_bin;

CREATE TABLE IF NOT EXISTS channel_secrets (
  channel_id  INTEGER NOT NULL PRIMARY KEY,
  ciphertext  MEDIUMTEXT NOT NULL,
  updated_at  VARCHAR(40) NOT NULL,
  CONSTRAINT fk_channel_secrets_channel FOREIGN KEY (channel_id) REFERENCES channels(id) ON DELETE CASCADE
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_bin;

CREATE TABLE IF NOT EXISTS feed_pool (
  url_key        CHAR(64) NOT NULL PRIMARY KEY,
  url            VARCHAR(2048) NOT NULL,
  source_type    VARCHAR(32) NOT NULL DEFAULT 'atom',
  etag           VARCHAR(255) NOT NULL DEFAULT '',
  last_modified  VARCHAR(255) NOT NULL DEFAULT '',
  last_checked   VARCHAR(40) NULL,
  last_version   VARCHAR(255) NOT NULL DEFAULT ''
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_bin;

CREATE TABLE IF NOT EXISTS seen_ids (
  url_key     CHAR(64) NOT NULL,
  release_id  VARCHAR(512) NOT NULL,
  seen_at     VARCHAR(40) NOT NULL,
  PRIMARY KEY (url_key, release_id)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_bin;

CREATE TABLE IF NOT EXISTS user_seen (
  user_id     INTEGER NOT NULL,
  url_key     CHAR(64) NOT NULL,
  release_id  VARCHAR(512) NOT NULL,
  seen_at     VARCHAR(40) NOT NULL,
  PRIMARY KEY (user_id, url_key, release_id),
  CONSTRAINT fk_user_seen_user FOREIGN KEY (user_id) REFERENCES users(id) ON DELETE CASCADE
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_bin;

CREATE TABLE IF NOT EXISTS notifications (
  id          INTEGER NOT NULL AUTO_INCREMENT PRIMARY KEY,
  cycle_id    VARCHAR(128) NOT NULL,
  merge_key   VARCHAR(128) NOT NULL DEFAULT '',
  kind        VARCHAR(32) NOT NULL,
  subject     VARCHAR(512) NOT NULL DEFAULT '',
  body        MEDIUMTEXT NOT NULL DEFAULT (''),
  html        MEDIUMTEXT NOT NULL DEFAULT (''),
  markdown    MEDIUMTEXT NOT NULL DEFAULT (''),
  created_at  VARCHAR(40) NOT NULL
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_bin;

CREATE TABLE IF NOT EXISTS delivery_groups (
  group_id          VARCHAR(128) NOT NULL PRIMARY KEY,
  cycle_id          VARCHAR(128) NOT NULL,
  merge_key         VARCHAR(128) NOT NULL,
  channel_type      VARCHAR(32) NOT NULL,
  idempotency_key   VARCHAR(255) NOT NULL,
  status            VARCHAR(32) NOT NULL DEFAULT 'pending',
  attempts          INTEGER NOT NULL DEFAULT 0,
  last_error        MEDIUMTEXT NOT NULL DEFAULT (''),
  created_at        VARCHAR(40) NOT NULL
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_bin;

CREATE TABLE IF NOT EXISTS deliveries (
  id           INTEGER NOT NULL AUTO_INCREMENT PRIMARY KEY,
  cycle_id     VARCHAR(128) NOT NULL,
  group_id     VARCHAR(128) NOT NULL,
  user_id      INTEGER NOT NULL,
  channel_id   INTEGER NOT NULL,
  kind         VARCHAR(32) NOT NULL,
  status       VARCHAR(32) NOT NULL DEFAULT 'pending',
  attempts     INTEGER NOT NULL DEFAULT 0,
  sent_at      VARCHAR(40) NULL,
  UNIQUE KEY uq_deliveries (cycle_id, user_id, channel_id, group_id, kind),
  CONSTRAINT fk_deliveries_user FOREIGN KEY (user_id) REFERENCES users(id) ON DELETE CASCADE
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_bin;

CREATE TABLE IF NOT EXISTS cycles (
  id           VARCHAR(128) NOT NULL PRIMARY KEY,
  started_at   VARCHAR(40) NOT NULL,
  finished_at  VARCHAR(40) NULL,
  urls         INTEGER NOT NULL DEFAULT 0,
  sent         INTEGER NOT NULL DEFAULT 0,
  failed       INTEGER NOT NULL DEFAULT 0
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_bin;

CREATE TABLE IF NOT EXISTS settings (
  setting_key  VARCHAR(191) NOT NULL PRIMARY KEY,
  value        MEDIUMTEXT NOT NULL DEFAULT (''),
  updated_at   VARCHAR(40) NOT NULL
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_bin;

CREATE TABLE IF NOT EXISTS audit_log (
  id           INTEGER NOT NULL AUTO_INCREMENT PRIMARY KEY,
  ts           VARCHAR(40) NOT NULL,
  actor_id     INTEGER NULL,
  action       VARCHAR(64) NOT NULL,
  target       VARCHAR(255) NOT NULL DEFAULT '',
  detail_json  MEDIUMTEXT NOT NULL DEFAULT (''),
  ip           VARCHAR(64) NOT NULL DEFAULT ''
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_bin;

CREATE TABLE IF NOT EXISTS catalog_categories (
  id          INTEGER NOT NULL AUTO_INCREMENT PRIMARY KEY,
  name        VARCHAR(191) NOT NULL UNIQUE,
  created_at  VARCHAR(40) NOT NULL,
  updated_at  VARCHAR(40) NOT NULL
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_bin;

CREATE TABLE IF NOT EXISTS catalog_entries (
  id             INTEGER NOT NULL AUTO_INCREMENT PRIMARY KEY,
  url            VARCHAR(1024) NOT NULL,
  url_key        CHAR(64) NOT NULL,
  name           VARCHAR(255) NOT NULL,
  category_id    INTEGER NULL,
  description    MEDIUMTEXT NOT NULL DEFAULT (''),
  homepage       VARCHAR(512) NOT NULL DEFAULT '',
  source_type    VARCHAR(32) NOT NULL DEFAULT 'atom',
  enabled        INTEGER NOT NULL DEFAULT 1,
  created_by     INTEGER NULL,
  created_at     VARCHAR(40) NOT NULL,
  updated_at     VARCHAR(40) NOT NULL,
  preset_key     VARCHAR(191) NOT NULL DEFAULT '',
  preset_version INTEGER NOT NULL DEFAULT 0,
  preset_edited  INTEGER NOT NULL DEFAULT 0,
  -- NULL for "not a preset", so the unique index ignores those rows.
  preset_key_live VARCHAR(191) GENERATED ALWAYS AS (NULLIF(preset_key, '')) STORED,
  CONSTRAINT fk_catalog_entries_category FOREIGN KEY (category_id)
    REFERENCES catalog_categories(id) ON DELETE SET NULL,
  CONSTRAINT fk_catalog_entries_creator FOREIGN KEY (created_by)
    REFERENCES users(id) ON DELETE SET NULL
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_bin;

CREATE TABLE IF NOT EXISTS submissions (
  id            INTEGER NOT NULL AUTO_INCREMENT PRIMARY KEY,
  kind          VARCHAR(16) NOT NULL,
  status        VARCHAR(16) NOT NULL DEFAULT 'pending',
  entry_id      INTEGER NULL,
  url           VARCHAR(1024) NOT NULL,
  url_key       CHAR(64) NOT NULL,
  name          VARCHAR(255) NOT NULL,
  category_id   INTEGER NULL,
  description   MEDIUMTEXT NOT NULL DEFAULT (''),
  homepage      VARCHAR(512) NOT NULL DEFAULT '',
  before_json   MEDIUMTEXT NOT NULL DEFAULT (''),
  submitted_by  INTEGER NULL,
  reject_reason VARCHAR(1024) NOT NULL DEFAULT '',
  reviewed_by   INTEGER NULL,
  reviewed_at   VARCHAR(40) NULL,
  created_at    VARCHAR(40) NOT NULL,
  updated_at    VARCHAR(40) NOT NULL,
  -- NULL unless the proposal is pending, so at most one pending row per URL.
  pending_url_key CHAR(64) GENERATED ALWAYS AS
                  (CASE WHEN status = 'pending' THEN url_key ELSE NULL END) STORED,
  CONSTRAINT ck_submissions_kind CHECK (kind IN ('new', 'update')),
  CONSTRAINT ck_submissions_status CHECK (status IN ('pending', 'approved', 'rejected', 'withdrawn')),
  CONSTRAINT fk_submissions_entry FOREIGN KEY (entry_id)
    REFERENCES catalog_entries(id) ON DELETE SET NULL,
  CONSTRAINT fk_submissions_category FOREIGN KEY (category_id)
    REFERENCES catalog_categories(id) ON DELETE SET NULL,
  CONSTRAINT fk_submissions_author FOREIGN KEY (submitted_by)
    REFERENCES users(id) ON DELETE SET NULL,
  CONSTRAINT fk_submissions_reviewer FOREIGN KEY (reviewed_by)
    REFERENCES users(id) ON DELETE SET NULL
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_bin;

CREATE TABLE IF NOT EXISTS catalog_preset_state (
  preset_key   VARCHAR(191) NOT NULL PRIMARY KEY,
  last_version INTEGER NOT NULL DEFAULT 0,
  deleted      INTEGER NOT NULL DEFAULT 0,
  updated_at   VARCHAR(40) NOT NULL
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_bin;

CREATE TABLE IF NOT EXISTS user_identities (
  id         INTEGER NOT NULL AUTO_INCREMENT PRIMARY KEY,
  user_id    INTEGER NOT NULL,
  provider   VARCHAR(32) NOT NULL,
  subject    VARCHAR(191) NOT NULL,
  login      VARCHAR(191) NOT NULL DEFAULT '',
  email      VARCHAR(255) NOT NULL DEFAULT '',
  created_at VARCHAR(40) NOT NULL,
  updated_at VARCHAR(40) NOT NULL,
  CONSTRAINT uq_user_identities_subject UNIQUE (provider, subject),
  CONSTRAINT uq_user_identities_user UNIQUE (provider, user_id),
  CONSTRAINT fk_user_identities_user FOREIGN KEY (user_id) REFERENCES users(id) ON DELETE CASCADE
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_bin;

CREATE TABLE IF NOT EXISTS auth_providers (
  provider      VARCHAR(32) NOT NULL PRIMARY KEY,
  enabled       INTEGER NOT NULL DEFAULT 0,
  client_id     VARCHAR(255) NOT NULL DEFAULT '',
  client_secret VARCHAR(1024) NOT NULL DEFAULT '',
  updated_at    VARCHAR(40) NOT NULL
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_bin;

CREATE TABLE IF NOT EXISTS oauth_flows (
  state_hash    CHAR(64) NOT NULL PRIMARY KEY,
  provider      VARCHAR(32) NOT NULL,
  mode          VARCHAR(16) NOT NULL DEFAULT 'login',
  user_id       INTEGER NULL,
  redirect      VARCHAR(512) NOT NULL DEFAULT '',
  code_verifier VARCHAR(255) NOT NULL DEFAULT '',
  created_at    VARCHAR(40) NOT NULL,
  expires_at    VARCHAR(40) NOT NULL,
  CONSTRAINT ck_oauth_flows_mode CHECK (mode IN ('login', 'bind')),
  CONSTRAINT fk_oauth_flows_user FOREIGN KEY (user_id) REFERENCES users(id) ON DELETE CASCADE
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_bin;
