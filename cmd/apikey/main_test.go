package main

import (
	"bytes"
	"context"
	"strconv"
	"strings"
	"testing"

	"api-gateway/internal/auth"
	"api-gateway/internal/storage"
	"api-gateway/internal/testdb"
)

func runCLI(t *testing.T, dbURL string, args ...string) (string, error) {
	t.Helper()
	var out bytes.Buffer
	err := run(context.Background(), args, dbURL, &out)
	return out.String(), err
}

// lastLine returns the final non-empty output line, where create prints the raw key.
func lastLine(s string) string {
	lines := strings.Split(strings.TrimSpace(s), "\n")
	return strings.TrimSpace(lines[len(lines)-1])
}

func TestUsageErrorsNeedNoDatabase(t *testing.T) {
	for name, args := range map[string][]string{
		"no command":        nil,
		"unknown command":   {"rotate"},
		"create no flags":   {"create"},
		"create bad email":  {"create", "-email", "nope", "-app", "x"},
		"create empty plan": {"create", "-email", "a@b.c", "-app", "x", "-plan", " "},
		"revoke no id":      {"revoke"},
		"unknown flag":      {"list", "-all"},
	} {
		if _, err := runCLI(t, "", args...); err == nil {
			t.Errorf("%s: expected error", name)
		}
	}
}

func TestCreateListRevoke(t *testing.T) {
	dbURL := testdb.URL(t)

	out, err := runCLI(t, dbURL, "create", "-email", "Dev@Example.com", "-app", "Checkout", "-plan", "pro")
	if err != nil {
		t.Fatalf("create: %v\n%s", err, out)
	}
	raw := lastLine(out)

	if _, err := runCLI(t, dbURL, "create", "-email", "x@example.com", "-app", "X", "-plan", "enterprise"); err == nil ||
		!strings.Contains(err.Error(), `unknown plan "enterprise"`) {
		t.Errorf("create with unknown plan: err = %v", err)
	}
	if !strings.HasPrefix(raw, "gw_") || len(raw) != 46 {
		t.Fatalf("create did not print a raw key; output:\n%s", out)
	}

	// Same owner (case-insensitive email) and app name reuse the application.
	out2, err := runCLI(t, dbURL, "create", "-email", "dev@example.com", "-app", "Checkout")
	if err != nil {
		t.Fatalf("second create: %v", err)
	}
	if raw2 := lastLine(out2); raw2 == raw {
		t.Fatal("second create returned the same key")
	}

	db, err := storage.Open(context.Background(), dbURL)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	key, err := db.LookupAPIKey(context.Background(), auth.HashKey(raw))
	if err != nil || key.Status != auth.StatusActive {
		t.Fatalf("created key not found as active: %+v, %v", key, err)
	}
	keys, err := db.ListAPIKeys(context.Background(), storage.KeyFilter{})
	if err != nil {
		t.Fatal(err)
	}
	if len(keys) != 2 || keys[0].ApplicationID != keys[1].ApplicationID || keys[0].Plan != "pro" {
		t.Fatalf("expected two keys on one 'pro' application, got %+v", keys)
	}

	list, err := runCLI(t, dbURL, "list")
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(list, raw[3:]) {
		t.Fatal("list output contains a raw key")
	}
	if !strings.Contains(list, "Checkout") || strings.Count(list, "active") != 2 {
		t.Errorf("unexpected list output:\n%s", list)
	}

	if _, err := runCLI(t, dbURL, "revoke", "-id", strconv.FormatInt(key.ID, 10)); err != nil {
		t.Fatalf("revoke: %v", err)
	}
	if key, _ = db.LookupAPIKey(context.Background(), auth.HashKey(raw)); key.Status != auth.StatusRevoked {
		t.Errorf("status after revoke = %q", key.Status)
	}
	if _, err := runCLI(t, dbURL, "revoke", "-id", "999999"); err == nil || !strings.Contains(err.Error(), "no API key with id 999999") {
		t.Errorf("revoke unknown id: err = %v", err)
	}

	list, _ = runCLI(t, dbURL, "list")
	if strings.Count(list, "revoked") != 1 {
		t.Errorf("list should show one revoked key:\n%s", list)
	}
}

func TestDatabaseUnavailable(t *testing.T) {
	_, err := runCLI(t, "postgres://u:pw@127.0.0.1:1/db", "list")
	if err == nil || !strings.Contains(err.Error(), "database unavailable") {
		t.Errorf("err = %v, want database unavailable", err)
	}
}
