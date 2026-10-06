package main

import (
	"context"
	"os"

	"github.com/hongle/hl-panel/internal/importers/ny/previewcmd"
)

func main() {
	os.Exit(previewcmd.Run(context.Background(), os.Args[1:], os.Stdout, os.Stderr))
}
