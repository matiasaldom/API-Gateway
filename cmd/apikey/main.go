// Command apikey issues, revokes, and lists gateway API keys.
//
//	apikey create -email dev@example.com -app "My App" [-plan free]
//	apikey revoke -id 42
//	apikey list
//
// Requires DATABASE_URL. Raw keys are printed once by create and are never stored or logged.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"strings"
	"text/tabwriter"
	"time"

	"api-gateway/internal/auth"
	"api-gateway/internal/storage"
)

const usage = `usage: apikey <command> [flags]

commands:
  create -email EMAIL -app NAME [-plan NAME]   issue a new key; creates the user/application if new
  revoke -id ID                                revoke a key (requests with it get 403)
  list                                         list keys (key values are never shown)

DATABASE_URL must point at the gateway database.`

func main() {
	if err := run(context.Background(), os.Args[1:], os.Getenv("DATABASE_URL"), os.Stdout); err != nil {
		if !errors.Is(err, flag.ErrHelp) {
			fmt.Fprintln(os.Stderr, "apikey:", err)
		}
		os.Exit(1)
	}
}

func run(ctx context.Context, args []string, databaseURL string, out io.Writer) error {
	if len(args) == 0 {
		return errors.New(usage)
	}
	cmd, args := args[0], args[1:]
	fs := flag.NewFlagSet("apikey "+cmd, flag.ContinueOnError)

	// Parse and validate flags before touching the database.
	var exec func(*storage.DB) error
	switch cmd {
	case "create":
		email := fs.String("email", "", "owner's email (user is created if new)")
		app := fs.String("app", "", "application name (created if new)")
		plan := fs.String("plan", "free", "plan for a newly created application")
		if err := fs.Parse(args); err != nil {
			return err
		}
		*email, *app, *plan = strings.TrimSpace(*email), strings.TrimSpace(*app), strings.TrimSpace(*plan)
		if !strings.Contains(*email, "@") || *app == "" || *plan == "" {
			return errors.New("create requires -email, -app, and a non-empty -plan")
		}
		exec = func(db *storage.DB) error {
			appID, err := db.EnsureApplication(ctx, *email, *app, *plan)
			if err != nil {
				return err
			}
			raw, id, err := db.CreateAPIKey(ctx, appID)
			if err != nil {
				return err
			}
			fmt.Fprintf(out, "Created API key %d for application %q (id %d, owner %s).\n", id, *app, appID, *email)
			fmt.Fprintf(out, "Copy it now; it cannot be shown again:\n\n%s\n", raw)
			return nil
		}

	case "revoke":
		id := fs.Int64("id", 0, "ID of the key to revoke (see: apikey list)")
		if err := fs.Parse(args); err != nil {
			return err
		}
		if *id <= 0 {
			return errors.New("revoke requires -id")
		}
		exec = func(db *storage.DB) error {
			if err := db.RevokeAPIKey(ctx, *id); errors.Is(err, auth.ErrKeyNotFound) {
				return fmt.Errorf("no API key with id %d", *id)
			} else if err != nil {
				return err
			}
			fmt.Fprintf(out, "Revoked API key %d.\n", *id)
			return nil
		}

	case "list":
		if err := fs.Parse(args); err != nil {
			return err
		}
		exec = func(db *storage.DB) error {
			keys, err := db.ListAPIKeys(ctx, storage.KeyFilter{})
			if err != nil {
				return err
			}
			tw := tabwriter.NewWriter(out, 0, 0, 2, ' ', 0)
			fmt.Fprintln(tw, "ID\tSTATUS\tAPP_ID\tAPPLICATION\tOWNER\tPLAN\tCREATED\tREVOKED")
			for _, k := range keys {
				revoked := "-"
				if k.RevokedAt != nil {
					revoked = k.RevokedAt.UTC().Format(time.RFC3339)
				}
				fmt.Fprintf(tw, "%d\t%s\t%d\t%s\t%s\t%s\t%s\t%s\n", k.ID, k.Status, k.ApplicationID,
					k.Application, k.OwnerEmail, k.Plan, k.CreatedAt.UTC().Format(time.RFC3339), revoked)
			}
			return tw.Flush()
		}

	case "-h", "-help", "--help", "help":
		fmt.Fprintln(out, usage)
		return nil
	default:
		return fmt.Errorf("unknown command %q\n\n%s", cmd, usage)
	}

	db, err := storage.Open(ctx, databaseURL)
	if err != nil {
		return fmt.Errorf("database unavailable: %w", err)
	}
	defer db.Close()
	return exec(db)
}
