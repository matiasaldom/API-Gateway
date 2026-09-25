// Package testdb provisions isolated PostgreSQL schemas for integration tests.
package testdb

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"net/url"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"testing"

	"github.com/jackc/pgx/v5"
)

// URL returns a database URL scoped to a fresh schema with all migrations applied,
// or skips the test when TEST_DATABASE_URL is unset. Each call gets its own schema,
// dropped on cleanup, so tests never touch existing tables.
func URL(t testing.TB) string {
	t.Helper()
	base := os.Getenv("TEST_DATABASE_URL")
	if base == "" {
		t.Skip("TEST_DATABASE_URL not set; skipping PostgreSQL integration test")
	}
	ctx := context.Background()

	var suffix [6]byte
	_, _ = rand.Read(suffix[:])
	schema := "gateway_test_" + hex.EncodeToString(suffix[:])

	admin, err := pgx.Connect(ctx, base)
	if err != nil {
		t.Fatalf("connect to TEST_DATABASE_URL: %v", err)
	}
	t.Cleanup(func() {
		_, _ = admin.Exec(context.Background(), "drop schema if exists "+schema+" cascade")
		_ = admin.Close(context.Background())
	})
	if _, err := admin.Exec(ctx, "create schema "+schema); err != nil {
		t.Fatal(err)
	}

	u, err := url.Parse(base)
	if err != nil {
		t.Fatalf("TEST_DATABASE_URL must be a URL: %v", err)
	}
	q := u.Query()
	q.Set("search_path", schema)
	u.RawQuery = q.Encode()
	scoped := u.String()

	conn, err := pgx.Connect(ctx, scoped)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close(ctx)

	files, err := filepath.Glob(filepath.Join(migrationsDir(), "*.up.sql"))
	if err != nil || len(files) == 0 {
		t.Fatalf("no migrations found in %s: %v", migrationsDir(), err)
	}
	sort.Strings(files)
	for _, f := range files {
		sql, err := os.ReadFile(f)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := conn.Exec(ctx, string(sql)); err != nil {
			t.Fatalf("apply %s: %v", filepath.Base(f), err)
		}
	}
	return scoped
}

// migrationsDir locates the repo's migrations regardless of which package's tests are running.
func migrationsDir() string {
	_, file, _, _ := runtime.Caller(0)
	return filepath.Join(filepath.Dir(file), "..", "..", "migrations")
}
