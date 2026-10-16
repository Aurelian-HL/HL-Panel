package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"

	"github.com/hongle/hl-panel/internal/gateway/membership"
)

type bundleConfig struct {
	Defaults  fileConfig `json:"defaults"`
	Listeners []struct {
		PoolID        string `json:"pool_id"`
		ListenAddress string `json:"listen_address"`
	} `json:"listeners"`
}

func loadBundle(path string) (bundleConfig, []runtimeConfig, error) {
	raw, err := readConfigFile(path)
	if err != nil {
		return bundleConfig{}, nil, err
	}
	var bundle bundleConfig
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if decoder.Decode(&bundle) != nil || decoder.Decode(new(any)) != io.EOF || len(bundle.Listeners) == 0 || len(bundle.Listeners) > 512 {
		return bundle, nil, errors.New("invalid gateway listener bundle")
	}
	pools, addresses := map[string]bool{}, map[string]bool{}
	configs := make([]runtimeConfig, 0, len(bundle.Listeners))
	for _, listener := range bundle.Listeners {
		if listener.PoolID == "" || listener.ListenAddress == "" || pools[listener.PoolID] || addresses[listener.ListenAddress] {
			return bundle, nil, errors.New("duplicate or empty pool/listener")
		}
		pools[listener.PoolID] = true
		addresses[listener.ListenAddress] = true
		input := bundle.Defaults
		input.ListenAddress = listener.ListenAddress
		raw, _ := json.Marshal(input)
		configuration, err := decodeRuntimeConfig(raw)
		if err != nil {
			return bundle, nil, err
		}
		if configuration.MembershipFile != "" {
			return bundle, nil, errors.New("bundle requires authenticated batch membership")
		}
		configs = append(configs, configuration)
	}
	return bundle, configs, nil
}

func runBundle(ctx context.Context, path string, logger *slog.Logger) error {
	bundle, configs, err := loadBundle(path)
	if err != nil {
		return err
	}
	source, err := membership.NewBatchHTTPSource(configs[0].MembershipURL, configs[0].MembershipTokenFile, configs[0].MembershipRefreshInterval)
	if err != nil {
		return err
	}
	// Validate every configured credential and response before opening listeners.
	for _, listener := range bundle.Listeners {
		if _, err := (membership.BatchPoolSource{Source: source, PoolID: listener.PoolID}).Load(ctx); err != nil {
			return fmt.Errorf("initialize batch membership: %w", err)
		}
	}
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	results := make(chan error, len(configs))
	for i, configuration := range configs {
		pool := bundle.Listeners[i].PoolID
		go func(configuration runtimeConfig, pool string) {
			results <- runGateway(ctx, configuration, membership.BatchPoolSource{Source: source, PoolID: pool}, logger.With("pool_id", pool))
		}(configuration, pool)
	}
	first := <-results
	cancel()
	for i := 1; i < len(configs); i++ {
		<-results
	}
	return first
}
