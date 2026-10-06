package previewcmd

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/hongle/hl-panel/internal/importers/ny"
)

func TestRunWritesSchemaRequiredRedactedReportToStdout(t *testing.T) {
	input := filepath.Join(t.TempDir(), "snapshot.json")
	if err := os.WriteFile(input, []byte(`{"rules":[{"target":"203.0.113.7:443","password":"do-not-print"}]}`), 0o600); err != nil {
		t.Fatalf("write fixture: %v", err)
	}
	var stdout bytes.Buffer
	var stderr bytes.Buffer
	exitCode := Run(context.Background(), []string{"-input", input, "-report", "-"}, &stdout, &stderr)
	if exitCode != ExitUnsupported {
		t.Fatalf("Run() exit = %d, want %d; stderr=%s", exitCode, ExitUnsupported, stderr.String())
	}
	var report ny.Report
	if err := json.Unmarshal(stdout.Bytes(), &report); err != nil {
		t.Fatalf("decode stdout report: %v\n%s", err, stdout.String())
	}
	if report.Status != ny.StatusSchemaRequired {
		t.Fatalf("report status = %q, want %q", report.Status, ny.StatusSchemaRequired)
	}
	for _, secret := range []string{"203.0.113.7", "do-not-print"} {
		if strings.Contains(stdout.String(), secret) {
			t.Fatalf("stdout report leaked %q: %s", secret, stdout.String())
		}
	}
}

func TestRunCreatesExplicitReportWithoutOverwriting(t *testing.T) {
	directory := t.TempDir()
	input := filepath.Join(directory, "snapshot.json")
	reportPath := filepath.Join(directory, "report.json")
	if err := os.WriteFile(input, []byte(`{"groups":[]}`), 0o600); err != nil {
		t.Fatalf("write fixture: %v", err)
	}
	var stderr bytes.Buffer
	firstExit := Run(context.Background(), []string{"-input", input, "-report", reportPath}, ioDiscard{}, &stderr)
	if firstExit != ExitUnsupported {
		t.Fatalf("first Run() exit = %d, want %d; stderr=%s", firstExit, ExitUnsupported, stderr.String())
	}
	first, err := os.ReadFile(reportPath)
	if err != nil {
		t.Fatalf("read report: %v", err)
	}
	if !bytes.Contains(first, []byte(`"status": "schema_required"`)) {
		t.Fatalf("unexpected report: %s", first)
	}

	stderr.Reset()
	secondExit := Run(context.Background(), []string{"-input", input, "-report", reportPath}, ioDiscard{}, &stderr)
	if secondExit != ExitInvalid || !strings.Contains(stderr.String(), "without overwriting") {
		t.Fatalf("second Run() exit/stderr = %d / %q", secondExit, stderr.String())
	}
	second, err := os.ReadFile(reportPath)
	if err != nil {
		t.Fatalf("read report after rejected overwrite: %v", err)
	}
	if !bytes.Equal(first, second) {
		t.Fatal("existing report changed after overwrite was rejected")
	}
}

func TestRunInvalidJSONStillEmitsReport(t *testing.T) {
	input := filepath.Join(t.TempDir(), "bad.json")
	if err := os.WriteFile(input, []byte(`{"rules":`), 0o600); err != nil {
		t.Fatalf("write fixture: %v", err)
	}
	var stdout bytes.Buffer
	exitCode := Run(context.Background(), []string{"-input", input}, &stdout, ioDiscard{})
	if exitCode != ExitInvalid {
		t.Fatalf("Run() exit = %d, want %d", exitCode, ExitInvalid)
	}
	var report ny.Report
	if err := json.Unmarshal(stdout.Bytes(), &report); err != nil {
		t.Fatalf("decode invalid report: %v", err)
	}
	if report.Status != ny.StatusInvalid {
		t.Fatalf("report status = %q, want %q", report.Status, ny.StatusInvalid)
	}
}

func TestRunRequiresCompleteExternalSchemaDeclaration(t *testing.T) {
	input := filepath.Join(t.TempDir(), "snapshot.json")
	if err := os.WriteFile(input, []byte(`{}`), 0o600); err != nil {
		t.Fatalf("write fixture: %v", err)
	}
	var stderr bytes.Buffer
	exitCode := Run(context.Background(), []string{"-input", input, "-schema-family", "ny-json"}, ioDiscard{}, &stderr)
	if exitCode != ExitInvalid || !strings.Contains(stderr.String(), "must be supplied together") {
		t.Fatalf("Run() exit/stderr = %d / %q", exitCode, stderr.String())
	}
}

func TestRunRejectsRelativeUNCAndDeviceReportPaths(t *testing.T) {
	input := filepath.Join(t.TempDir(), "snapshot.json")
	if err := os.WriteFile(input, []byte(`{}`), 0o600); err != nil {
		t.Fatalf("write fixture: %v", err)
	}
	for name, reportPath := range map[string]string{
		"empty":     "",
		"relative":  "report.json",
		"UNC":       `\\definitely-not-a-real-host.invalid\share\report.json`,
		"device":    `\\.\NUL`,
		"namespace": `\\?\C:\reports\report.json`,
	} {
		t.Run(name, func(t *testing.T) {
			var stderr bytes.Buffer
			exitCode := Run(context.Background(), []string{"-input", input, "-report", reportPath}, ioDiscard{}, &stderr)
			if exitCode != ExitInvalid {
				t.Fatalf("Run() exit = %d, want %d; stderr=%s", exitCode, ExitInvalid, stderr.String())
			}
		})
	}
}

type ioDiscard struct{}

func (ioDiscard) Write(value []byte) (int, error) { return len(value), nil }
