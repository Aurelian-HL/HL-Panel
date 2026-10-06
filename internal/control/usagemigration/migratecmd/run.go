// Package migratecmd provides the command-line boundary for usage-ledger
// migration operations. It never logs or returns a configured DSN.
package migratecmd

import (
	"context"
	"database/sql"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"strings"
	"time"

	"github.com/hongle/hl-panel/internal/control/usagemigration"
	_ "github.com/jackc/pgx/v5/stdlib"
)

const defaultTimeout = 30 * time.Second

type options struct {
	dsnFile string
	dsn     string
	force   bool
	timeout time.Duration
}

func Run(ctx context.Context, args []string, stdout, stderr io.Writer) int {
	if len(args) == 0 {
		printUsage(stderr)
		return 2
	}
	action := args[0]
	if action != "status" && action != "apply" && action != "verify" && action != "rollback" {
		fmt.Fprintln(stderr, "unknown usage migration command")
		printUsage(stderr)
		return 2
	}
	var cfg options
	flags := flag.NewFlagSet("usage-migrate "+action, flag.ContinueOnError)
	flags.SetOutput(stderr)
	flags.StringVar(&cfg.dsnFile, "dsn-file", "", "path to a protected file containing the PostgreSQL DSN")
	flags.StringVar(&cfg.dsn, "dsn", "", "PostgreSQL DSN fallback (prefer -dsn-file)")
	flags.BoolVar(&cfg.force, "force", false, "allow rollback even when usage-ledger data exists")
	flags.DurationVar(&cfg.timeout, "timeout", defaultTimeout, "operation timeout")
	if err := flags.Parse(args[1:]); err != nil {
		return 2
	}
	if flags.NArg() != 0 {
		fmt.Fprintln(stderr, "unexpected positional arguments")
		return 2
	}
	if cfg.force && action != "rollback" {
		fmt.Fprintln(stderr, "-force is valid only for rollback")
		return 2
	}
	if cfg.timeout <= 0 {
		fmt.Fprintln(stderr, "-timeout must be positive")
		return 2
	}
	dsn, err := resolveDSN(cfg)
	if err != nil {
		fmt.Fprintf(stderr, "usage migration configuration failed: %v\n", err)
		return 2
	}
	db, err := sql.Open("pgx", dsn)
	if err != nil {
		fmt.Fprintln(stderr, "open PostgreSQL connection failed")
		return 1
	}
	defer db.Close()
	operationContext, cancel := context.WithTimeout(ctx, cfg.timeout)
	defer cancel()
	if err := db.PingContext(operationContext); err != nil {
		fmt.Fprintln(stderr, "connect PostgreSQL failed")
		return 1
	}
	runner, err := usagemigration.New(db)
	if err != nil {
		fmt.Fprintf(stderr, "initialize usage migration failed: %v\n", err)
		return 1
	}

	switch action {
	case "status":
		status, err := runner.Status(operationContext)
		if err != nil {
			return reportFailure(stderr, action, err)
		}
		fmt.Fprintf(stdout, "status=%s version=%d", status.State, status.Version)
		if status.Detail != "" {
			fmt.Fprintf(stdout, " detail=%q", status.Detail)
		}
		fmt.Fprintln(stdout)
		if status.State == usagemigration.StateDrifted || status.State == usagemigration.StateUnmanaged {
			return 1
		}
		return 0
	case "apply":
		result, err := runner.Apply(operationContext)
		if err != nil {
			return reportFailure(stderr, action, err)
		}
		outcome := "already_applied"
		if result.Applied {
			outcome = "applied"
		}
		fmt.Fprintf(stdout, "status=%s version=%d\n", outcome, result.Version)
		return 0
	case "verify":
		if err := runner.Verify(operationContext); err != nil {
			return reportFailure(stderr, action, err)
		}
		fmt.Fprintf(stdout, "status=verified version=%d\n", usagemigration.Version)
		return 0
	case "rollback":
		result, err := runner.Rollback(operationContext, cfg.force)
		if err != nil {
			return reportFailure(stderr, action, err)
		}
		fmt.Fprintf(stdout, "status=rolled_back version=%d forced=%t\n", result.Version, cfg.force)
		return 0
	default:
		panic("validated command was not dispatched")
	}
}

func resolveDSN(cfg options) (string, error) {
	filePath := firstNonEmpty(
		cfg.dsnFile,
		os.Getenv("USAGE_MIGRATION_DSN_FILE"),
		os.Getenv("CONTROL_DATABASE_URL_FILE"),
		os.Getenv("DSN_FILE"),
	)
	if filePath != "" {
		raw, err := os.ReadFile(filePath)
		if err != nil {
			return "", errors.New("read DSN file failed")
		}
		dsn := strings.TrimSpace(string(raw))
		if dsn == "" {
			return "", errors.New("DSN file is empty")
		}
		return dsn, nil
	}
	dsn := firstNonEmpty(
		cfg.dsn,
		os.Getenv("USAGE_MIGRATION_DSN"),
		os.Getenv("CONTROL_DATABASE_URL"),
	)
	if dsn == "" {
		return "", errors.New("set -dsn-file, USAGE_MIGRATION_DSN_FILE, CONTROL_DATABASE_URL_FILE, or a direct DSN fallback")
	}
	return dsn, nil
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if value = strings.TrimSpace(value); value != "" {
			return value
		}
	}
	return ""
}

func reportFailure(stderr io.Writer, action string, err error) int {
	fmt.Fprintf(stderr, "usage migration %s failed: %v\n", action, err)
	return 1
}

func printUsage(writer io.Writer) {
	fmt.Fprintln(writer, "usage: usage-migrate <status|apply|verify|rollback> [-dsn-file path] [-timeout 30s] [-force]")
}
