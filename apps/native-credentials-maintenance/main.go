// Local-only, stdin-fed credential maintenance for an imported native rule.
package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"os"
	"strings"
	"time"

	"github.com/hongle/hl-panel/internal/control/auth"
	"github.com/hongle/hl-panel/internal/control/memoryrepo"
	"github.com/hongle/hl-panel/internal/control/postgressnapshot"
	"github.com/hongle/hl-panel/internal/control/vlessruntime"
)

func main() {
	mode := flag.String("mode", "rehearse", "rehearse or apply")
	dsnFile := flag.String("dsn-file", "", "private file containing database URL")
	snapshot := flag.String("snapshot", "", "private snapshot file, rehearsal only")
	flag.Parse()
	fail := func(stage string) { fmt.Fprintln(os.Stderr, stage+" failed; no credentials emitted"); os.Exit(1) }
	var input vlessruntime.NativeCredentialsRotation
	d := json.NewDecoder(io.LimitReader(os.Stdin, 1<<20))
	d.DisallowUnknownFields()
	if d.Decode(&input) != nil {
		fail("input")
	}
	var extra any
	if d.Decode(&extra) != io.EOF {
		fail("input trailing data")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	var result vlessruntime.NativeCredentialsResult
	var err error
	switch *mode {
	case "rehearse":
		raw, e := os.ReadFile(*snapshot)
		if e != nil {
			fail("snapshot read")
		}
		s, e := memoryrepo.DecodeSnapshot(raw)
		if e != nil {
			fail("snapshot decode")
		}
		result, err = s.RotateNativeSOCKS5Credentials(ctx, input)
		if err == nil {
			var b []byte
			b, err = s.EncodeSnapshot()
			if err == nil {
				_, err = memoryrepo.DecodeSnapshot(b)
			}
		}
	case "apply":
		st, e := os.Stat(*dsnFile)
		if e != nil || st.Mode().Perm()&0077 != 0 {
			fail("private database file")
		}
		raw, e := os.ReadFile(*dsnFile)
		if e != nil {
			fail("database file")
		}
		s, e := postgressnapshot.Open(ctx, strings.TrimSpace(string(raw)), auth.Administrator{})
		if e != nil {
			fail("database open")
		}
		defer s.Close()
		result, err = s.RotateNativeSOCKS5Credentials(ctx, input)
	default:
		fail("mode")
	}
	if err != nil {
		fail("credential rotation")
	}
	json.NewEncoder(os.Stdout).Encode(struct {
		Mode   string                               `json:"mode"`
		Result vlessruntime.NativeCredentialsResult `json:"result"`
	}{*mode, result})
}
