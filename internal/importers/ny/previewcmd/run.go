// Package previewcmd implements the offline ny-import-preview command. It has
// no network client and no database dependency.
package previewcmd

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/hongle/hl-panel/internal/importers/ny"
)

const (
	ExitReady       = 0
	ExitInvalid     = 1
	ExitUnsupported = 2
)

func Run(ctx context.Context, args []string, stdout, stderr io.Writer) int {
	if stdout == nil {
		stdout = io.Discard
	}
	if stderr == nil {
		stderr = io.Discard
	}
	flags := flag.NewFlagSet("ny-import-preview", flag.ContinueOnError)
	flags.SetOutput(stderr)
	inputPath := flags.String("input", "", "local NY snapshot path (read-only)")
	reportPath := flags.String("report", "-", "report path, or - for stdout")
	schemaFamily := flags.String("schema-family", "", "externally verified schema family")
	schemaVersion := flags.String("schema-version", "", "externally verified schema version")
	if err := flags.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return ExitReady
		}
		return ExitInvalid
	}
	if flags.NArg() != 0 {
		fmt.Fprintln(stderr, "unexpected positional arguments")
		return ExitInvalid
	}
	if strings.TrimSpace(*inputPath) == "" {
		fmt.Fprintln(stderr, "-input is required and must identify a local snapshot")
		return ExitInvalid
	}
	if (strings.TrimSpace(*schemaFamily) == "") != (strings.TrimSpace(*schemaVersion) == "") {
		fmt.Fprintln(stderr, "-schema-family and -schema-version must be supplied together")
		return ExitInvalid
	}

	inspector, err := ny.NewInspector(ny.Options{})
	if err != nil {
		fmt.Fprintf(stderr, "initialize offline inspector: %v\n", err)
		return ExitInvalid
	}
	snapshot, err := inspector.ReadSnapshot(ctx, *inputPath)
	if err != nil {
		fmt.Fprintf(stderr, "read local snapshot: %v\n", err)
		return ExitInvalid
	}
	report := inspector.Preflight(ctx, snapshot, ny.SchemaDeclaration{
		Family:  *schemaFamily,
		Version: *schemaVersion,
	})
	encoded, err := json.MarshalIndent(report, "", "  ")
	if err != nil {
		fmt.Fprintf(stderr, "encode redacted report: %v\n", err)
		return ExitInvalid
	}
	encoded = append(encoded, '\n')
	if err := writeReport(*reportPath, encoded, stdout); err != nil {
		fmt.Fprintf(stderr, "write redacted report: %v\n", err)
		return ExitInvalid
	}

	switch report.Status {
	case ny.StatusPreviewReady:
		return ExitReady
	case ny.StatusSchemaRequired, ny.StatusUnsupported:
		return ExitUnsupported
	default:
		return ExitInvalid
	}
}

func writeReport(path string, report []byte, stdout io.Writer) error {
	path = strings.TrimSpace(path)
	if path == "-" {
		_, err := io.Copy(stdout, bytes.NewReader(report))
		return err
	}
	if path == "" {
		return errors.New("report path must be an absolute local path or - for stdout")
	}
	absolute, err := ny.ValidateNewLocalFilePath(path)
	if err != nil {
		return err
	}
	file, err := os.OpenFile(absolute, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return fmt.Errorf("create new report file without overwriting: %w", err)
	}
	written := false
	defer func() {
		_ = file.Close()
		if !written {
			_ = os.Remove(absolute)
		}
	}()
	if _, err := file.Write(report); err != nil {
		return err
	}
	if err := file.Sync(); err != nil {
		return err
	}
	if err := file.Close(); err != nil {
		return err
	}
	written = true
	return nil
}
