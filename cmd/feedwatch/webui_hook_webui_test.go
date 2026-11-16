//go:build webui

package main

import (
	"path/filepath"
	"testing"

	"feedwatch/internal/webui/store"
)

func TestRedactDSNHidesPassword(t *testing.T) {
	cases := []struct {
		dialect  store.Dialect
		in, want string
	}{
		{store.SQLite, "data/feedwatch.db", "data/feedwatch.db"},
		{store.MySQL, "fw:s3cret@tcp(db:3306)/feedwatch?charset=utf8mb4", "fw:***@tcp(db:3306)/feedwatch?charset=utf8mb4"},
		{store.Postgres, "postgres://fw:s3cret@db:5432/feedwatch?sslmode=disable", "postgres://fw:***@db:5432/feedwatch?sslmode=disable"},
		// A DSN without a password must not be mangled.
		{store.Postgres, "postgres://fw@db:5432/feedwatch", "postgres://fw@db:5432/feedwatch"},
		{store.Postgres, "host=db user=fw dbname=fw", "host=db user=fw dbname=fw"},
	}
	for _, tc := range cases {
		got := redactDSN(store.Config{Dialect: tc.dialect, DSN: tc.in})
		if got != tc.want {
			t.Errorf("redactDSN(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

func TestListeningURLIsClickable(t *testing.T) {
	cases := []struct {
		addr, want string
	}{
		{":8080", "http://localhost:8080"},
		{"127.0.0.1:8080", "http://127.0.0.1:8080"},
		{"localhost:18091", "http://localhost:18091"},
		{"0.0.0.0:8080", "http://localhost:8080"},
		{"[::]:8080", "http://localhost:8080"},
		{"[::1]:8080", "http://[::1]:8080"},
		{"192.168.1.10:80", "http://192.168.1.10:80"},
	}
	for _, tc := range cases {
		if got := listeningURL(tc.addr); got != tc.want {
			t.Errorf("listeningURL(%q) = %q, want %q", tc.addr, got, tc.want)
		}
	}
}

func TestDatabaseConfigDefaultsToSQLite(t *testing.T) {
	t.Setenv("FEEDWATCH_DB_DRIVER", "")
	t.Setenv("FEEDWATCH_DB_DSN", "")
	t.Setenv("FEEDWATCH_DB_PATH", "")
	cfg, err := databaseConfig()
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Dialect != store.SQLite || cfg.DSN != "data/feedwatch.db" {
		t.Fatalf("cfg = %+v, want sqlite data/feedwatch.db", cfg)
	}
}

func TestDatabaseConfigRequiresDSNForNetworkBackend(t *testing.T) {
	t.Setenv("FEEDWATCH_DB_DRIVER", "mysql")
	t.Setenv("FEEDWATCH_DB_DSN", "")
	t.Setenv("FEEDWATCH_DB_PATH", "data/feedwatch.db")
	if _, err := databaseConfig(); err == nil {
		t.Fatal("a network driver without a DSN must fail")
	}
}

func TestDatabaseConfigReadsPoolSize(t *testing.T) {
	t.Setenv("FEEDWATCH_DB_DRIVER", "postgres")
	t.Setenv("FEEDWATCH_DB_DSN", "postgres://fw@db/fw")
	t.Setenv("FEEDWATCH_DB_MAX_OPEN_CONNS", "25")
	cfg, err := databaseConfig()
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Dialect != store.Postgres || cfg.MaxOpenConns != 25 {
		t.Fatalf("cfg = %+v, want postgres with 25 connections", cfg)
	}
	t.Setenv("FEEDWATCH_DB_MAX_OPEN_CONNS", "0")
	if _, err := databaseConfig(); err == nil {
		t.Fatal("a non-positive pool size must fail")
	}
}

func TestDataDirFollowsDatabaseLocation(t *testing.T) {
	t.Setenv("FEEDWATCH_DATA_DIR", "")
	want := filepath.Dir(filepath.FromSlash("var/lib/fw/feedwatch.db"))
	if got := dataDir(store.Config{Dialect: store.SQLite, DSN: "var/lib/fw/feedwatch.db"}); got != want {
		t.Errorf("sqlite data dir = %q, want %q", got, want)
	}
	if got := dataDir(store.Config{Dialect: store.Postgres, DSN: "postgres://fw@db/fw"}); got != "data" {
		t.Errorf("network data dir = %q", got)
	}
	t.Setenv("FEEDWATCH_DATA_DIR", "/srv/fw")
	if got := dataDir(store.Config{Dialect: store.Postgres, DSN: "postgres://fw@db/fw"}); got != "/srv/fw" {
		t.Errorf("explicit data dir = %q", got)
	}
}
