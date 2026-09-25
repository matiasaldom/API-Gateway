package storage

import (
	"context"
	"strings"
	"testing"
	"time"
)

func TestOpenFailsFastWhenDatabaseUnavailable(t *testing.T) {
	start := time.Now()
	// Nothing listens on port 1; the password must not surface in the error.
	_, err := Open(context.Background(), "postgres://gateway:s3cret-pw@127.0.0.1:1/gateway?sslmode=disable")
	if err == nil {
		t.Fatal("expected error for unreachable database")
	}
	if elapsed := time.Since(start); elapsed > connectTimeout+time.Second {
		t.Errorf("Open took %s, want at most ~%s", elapsed, connectTimeout)
	}
	if strings.Contains(err.Error(), "s3cret-pw") {
		t.Errorf("error leaks password: %v", err)
	}
}

func TestOpenRejectsBadURL(t *testing.T) {
	for _, u := range []string{"", "postgres://gateway:s3cret-pw@host:notaport/db"} {
		_, err := Open(context.Background(), u)
		if err == nil {
			t.Errorf("Open(%q): expected error", u)
		} else if strings.Contains(err.Error(), "s3cret-pw") {
			t.Errorf("error leaks password: %v", err)
		}
	}
}
