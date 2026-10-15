package engine

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// This subprocess replaces an engine validator without opening any sockets or
// touching a real engine. TestMain runs before testing parses the engine argv.
func TestMain(m *testing.M) {
	if os.Getenv("HL_PANEL_VALIDATION_HELPER") == "1" {
		mode := os.Args[len(os.Args)-1]
		if os.Args[1] == "-C" {
			mode = os.Args[2]
		}
		switch mode {
		case "valid":
			os.Exit(0)
		case "invalid":
			fmt.Fprintln(os.Stderr, "sensitive validation diagnostic")
			os.Exit(1)
		default:
			if err := os.WriteFile(mode, []byte("started"), 0o600); err != nil {
				os.Exit(2)
			}
			time.Sleep(time.Minute)
			os.Exit(0)
		}
	}
	os.Exit(m.Run())
}

func TestExecEngineValidationIsBoundedAndRedacted(t *testing.T) {
	t.Setenv("HL_PANEL_VALIDATION_HELPER", "1")
	binary, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	runners := []struct {
		name string
		test func(context.Context, string) error
		want error
	}{
		{"xray", (&ExecXrayRunner{binaryPath: binary}).Test, ErrXrayValidation},
		{"gost", (&ExecGOSTRunner{binaryPath: binary}).Test, ErrGOSTValidation},
	}
	for _, runner := range runners {
		t.Run(runner.name, func(t *testing.T) {
			if err := runner.test(context.Background(), "valid"); err != nil {
				t.Fatalf("valid config: %v", err)
			}
			if err := runner.test(context.Background(), "invalid"); err != runner.want {
				t.Fatalf("diagnostic must be redacted: %v", err)
			}
			marker := filepath.Join(t.TempDir(), "validator-started")
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			done := make(chan error, 1)
			started := time.Now()
			go func() { done <- runner.test(ctx, marker) }()
			select {
			case err := <-done:
				if err != runner.want {
					t.Fatalf("hung validator error: %v", err)
				}
			case <-time.After(25 * time.Second):
				cancel()
				<-done
				t.Fatal("hung validator retained the engine lifecycle lock beyond the validation budget")
			}
			if time.Since(started) < engineValidationTimeout-2*time.Second {
				t.Fatal("helper did not exercise the internal validation deadline")
			}
			if _, err := os.Stat(marker); err != nil {
				t.Fatalf("validator was never launched: %v", err)
			}
			parent, stop := context.WithTimeout(context.Background(), 200*time.Millisecond)
			defer stop()
			started = time.Now()
			if err := runner.test(parent, marker); err != runner.want || time.Since(started) > 3*time.Second {
				t.Fatalf("shorter parent deadline not respected: %v", err)
			}
			cancelled, stopNow := context.WithCancel(context.Background())
			stopNow()
			if err := runner.test(cancelled, marker); !errors.Is(err, context.Canceled) {
				t.Fatalf("already cancelled parent: %v", err)
			}
		})
	}
}
