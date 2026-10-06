package main

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"strings"
	"time"

	"github.com/hongle/hl-panel/internal/control/audit"
	"github.com/hongle/hl-panel/internal/control/auth"
)

// This local maintenance command requires access to the private database
// configuration. It is never exposed through the HTTP API.
func resetAdministratorPassword(username string, input io.Reader) error {
	config, err := loadRuntimeConfig()
	if err != nil {
		return err
	}
	if config.DatabaseURL == "" {
		return fmt.Errorf("password reset requires a persistent database")
	}
	line, err := bufio.NewReader(io.LimitReader(input, 258)).ReadString('\n')
	if err != nil && err != io.EOF {
		return fmt.Errorf("read password from stdin failed")
	}
	password := strings.TrimSuffix(strings.TrimSuffix(line, "\n"), "\r")
	if password != auth.DefaultPassword && (len(password) < 8 || len(password) > 256) {
		return fmt.Errorf("new password must have 8 to 256 characters, or be the initial default")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	store, closeStore, err := openRepository(ctx, config, auth.Administrator{})
	if err != nil {
		return err
	}
	defer closeStore()
	admin, err := store.AdministratorByUsername(ctx, username)
	if err != nil {
		return fmt.Errorf("administrator does not exist")
	}
	hash, err := auth.HashPassword(password)
	if err != nil {
		return err
	}
	event, err := audit.NewEvent(time.Now().UTC(), "local_operator", "maintenance", "auth.password.reset", "administrator", admin.ID, "succeeded", nil)
	if err != nil {
		return err
	}
	return store.UpdateAdministratorPassword(ctx, admin.ID, admin.PasswordHash, hash, password == auth.DefaultPassword, event)
}
