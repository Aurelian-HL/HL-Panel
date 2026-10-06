package main

import (
	"context"
	"database/sql"
	"errors"
	"time"

	"github.com/hongle/hl-panel/internal/control/usage"
	usagememory "github.com/hongle/hl-panel/internal/control/usage/memory"
	usagepostgres "github.com/hongle/hl-panel/internal/control/usage/postgres"
	"github.com/hongle/hl-panel/internal/control/usagemigration"
	_ "github.com/jackc/pgx/v5/stdlib"
)

func openUsageRepository(ctx context.Context, config runtimeConfig) (usage.Repository, func(), error) {
	if config.DatabaseURL == "" {
		return usagememory.New(), func() {}, nil
	}
	db, err := sql.Open("pgx", config.DatabaseURL)
	if err != nil {
		return nil, nil, errors.New("invalid usage PostgreSQL connection configuration")
	}
	db.SetMaxOpenConns(4)
	db.SetMaxIdleConns(2)
	db.SetConnMaxLifetime(30 * time.Minute)
	closeRepository := func() { _ = db.Close() }
	runner, err := usagemigration.New(db)
	if err != nil {
		closeRepository()
		return nil, nil, errors.New("usage schema verifier initialization failed")
	}
	if err := runner.Verify(ctx); err != nil {
		closeRepository()
		return nil, nil, errors.New("usage schema is not ready; run usage-migrate apply and verify before starting control-api")
	}
	return usagepostgres.New(db), closeRepository, nil
}
