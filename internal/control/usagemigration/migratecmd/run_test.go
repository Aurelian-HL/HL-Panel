package migratecmd

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestResolveDSNFileTakesPriorityAndTrimsContent(t *testing.T) {
	t.Setenv("USAGE_MIGRATION_DSN_FILE", "")
	t.Setenv("CONTROL_DATABASE_URL_FILE", "")
	t.Setenv("DSN_FILE", "")
	t.Setenv("USAGE_MIGRATION_DSN", "postgres://direct-secret@invalid/direct")
	path := filepath.Join(t.TempDir(), "database-url")
	if err := os.WriteFile(path, []byte("  postgres://file-secret@invalid/file\r\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	dsn, err := resolveDSN(options{dsnFile: path, dsn: "postgres://flag-secret@invalid/flag"})
	if err != nil {
		t.Fatal(err)
	}
	if dsn != "postgres://file-secret@invalid/file" {
		t.Fatalf("file did not take priority or was not trimmed: %q", dsn)
	}
}

func TestResolveDSNErrorNeverContainsSecretOrFileContent(t *testing.T) {
	for _, name := range []string{"USAGE_MIGRATION_DSN_FILE", "CONTROL_DATABASE_URL_FILE", "DSN_FILE", "USAGE_MIGRATION_DSN", "CONTROL_DATABASE_URL"} {
		t.Setenv(name, "")
	}
	path := filepath.Join(t.TempDir(), "empty-dsn")
	if err := os.WriteFile(path, []byte(" \n"), 0o600); err != nil {
		t.Fatal(err)
	}
	_, err := resolveDSN(options{dsnFile: path, dsn: "postgres://must-not-appear@invalid/database"})
	if err == nil {
		t.Fatal("empty prioritized DSN file was accepted")
	}
	if strings.Contains(err.Error(), "must-not-appear") || strings.Contains(err.Error(), path) {
		t.Fatalf("configuration error disclosed sensitive input: %v", err)
	}
}

func TestRunRejectsInvalidInvocationBeforeConnecting(t *testing.T) {
	t.Setenv("USAGE_MIGRATION_DSN_FILE", "")
	t.Setenv("CONTROL_DATABASE_URL_FILE", "")
	t.Setenv("DSN_FILE", "")
	t.Setenv("USAGE_MIGRATION_DSN", "")
	t.Setenv("CONTROL_DATABASE_URL", "")
	tests := []struct {
		name string
		args []string
	}{
		{name: "missing command"},
		{name: "unknown command", args: []string{"destroy"}},
		{name: "force apply", args: []string{"apply", "-force"}},
		{name: "unexpected argument", args: []string{"status", "extra"}},
		{name: "invalid timeout", args: []string{"verify", "-timeout=0"}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			var stdout, stderr bytes.Buffer
			if code := Run(context.Background(), test.args, &stdout, &stderr); code != 2 {
				t.Fatalf("exit code = %d, stderr=%q", code, stderr.String())
			}
			if stdout.Len() != 0 {
				t.Fatalf("unexpected stdout: %q", stdout.String())
			}
		})
	}
}

func TestRunDoesNotPrintDirectDSNWhenConnectionFails(t *testing.T) {
	for _, name := range []string{"USAGE_MIGRATION_DSN_FILE", "CONTROL_DATABASE_URL_FILE", "DSN_FILE", "USAGE_MIGRATION_DSN", "CONTROL_DATABASE_URL"} {
		t.Setenv(name, "")
	}
	const secret = "migration-password-must-not-appear"
	var stdout, stderr bytes.Buffer
	code := Run(context.Background(), []string{
		"status",
		"-dsn=postgres://migration-user:" + secret + "@127.0.0.1:1/nyvp_unreachable?sslmode=disable",
		"-timeout=250ms",
	}, &stdout, &stderr)
	if code != 1 {
		t.Fatalf("exit code = %d, stdout=%q, stderr=%q", code, stdout.String(), stderr.String())
	}
	if strings.Contains(stderr.String(), secret) || strings.Contains(stderr.String(), "postgres://") {
		t.Fatalf("connection error disclosed DSN: %q", stderr.String())
	}
}
