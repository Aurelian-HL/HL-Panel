package state

import (
	"errors"
	"os"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/hongle/hl-panel/internal/protocol/agentv1"
)

func TestCheckDesiredRejectsInvalidReplaysWithoutWritingState(t *testing.T) {
	store := newTestStore(t.TempDir())
	applied := testDesired(2, `{"value":"applied"}`)
	if _, err := store.CheckDesired(applied); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(store.statePath); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("preflight created state: %v", err)
	}
	if _, err := store.Begin(applied, time.Now()); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Complete(applied, time.Now()); err != nil {
		t.Fatal(err)
	}
	pending := testDesired(4, `{"value":"pending"}`)
	if _, err := store.Begin(pending, time.Now()); err != nil {
		t.Fatal(err)
	}
	before, err := os.ReadFile(store.statePath)
	if err != nil {
		t.Fatal(err)
	}
	beforeFiles, err := os.ReadDir(store.configurationDir)
	if err != nil {
		t.Fatal(err)
	}
	altered := testDesired(2, `{"value":"altered"}`)
	altered.ConfigSHA256 = applied.ConfigSHA256
	uppercase := applied
	uppercase.ConfigSHA256 = " " + strings.ToUpper(applied.ConfigSHA256) + " "
	for _, test := range []struct {
		name    string
		desired agentv1.DesiredNodeConfig
		wantErr error
	}{
		{"altered body", altered, ErrConfigurationHash},
		{"older applied", testDesired(1, `{}`), ErrGenerationRollback},
		{"applied conflict", testDesired(2, `{}`), ErrGenerationConflict},
		{"older pending", testDesired(3, `{}`), ErrGenerationRollback},
		{"pending conflict", testDesired(4, `{}`), ErrGenerationConflict},
		{"newer while pending", testDesired(5, `{}`), ErrPendingApplyExists},
		{"identical applied", applied, nil},
		{"normalized applied hash", uppercase, nil},
		{"identical pending", pending, nil},
	} {
		t.Run(test.name, func(t *testing.T) {
			if _, err := store.CheckDesired(test.desired); !errors.Is(err, test.wantErr) {
				t.Fatalf("preflight error = %v, want %v", err, test.wantErr)
			}
		})
	}
	after, err := os.ReadFile(store.statePath)
	if err != nil {
		t.Fatal(err)
	}
	afterFiles, err := os.ReadDir(store.configurationDir)
	if err != nil {
		t.Fatal(err)
	}
	if string(before) != string(after) || !reflect.DeepEqual(beforeFiles, afterFiles) {
		t.Fatal("preflight changed state or configuration files")
	}
}
