package main

import (
	"context"
	"os"

	"github.com/hongle/hl-panel/internal/control/usagemigration/migratecmd"
)

func main() {
	os.Exit(migratecmd.Run(context.Background(), os.Args[1:], os.Stdout, os.Stderr))
}
