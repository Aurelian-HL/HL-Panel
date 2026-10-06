package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/hongle/hl-panel/internal/routing/endpointrouter"
)

const maxGatewayConfigBytes int64 = 64 << 10

type runtimeConfig struct {
	ListenAddress             string
	AllowNonLoopback          bool
	SelectionPolicy           endpointrouter.SelectionPolicy
	MembershipFile            string
	MembershipURL             string
	MembershipTokenFile       string
	MembershipRefreshInterval time.Duration
	DialTimeout               time.Duration
	TCPReachability           tcpReachabilityConfig
}

type tcpReachabilityConfig struct {
	Enabled          bool
	Interval         time.Duration
	Timeout          time.Duration
	FailureThreshold int
	SuccessThreshold int
	MaxConcurrency   int
}

type fileConfig struct {
	ListenAddress             string `json:"listen_address"`
	AllowNonLoopback          bool   `json:"allow_non_loopback"`
	SelectionPolicy           string `json:"selection_policy"`
	MembershipFile            string `json:"membership_file"`
	MembershipURL             string `json:"membership_url"`
	MembershipTokenFile       string `json:"membership_token_file"`
	MembershipRefreshInterval string `json:"membership_refresh_interval"`
	DialTimeout               string `json:"dial_timeout"`
	TCPReachability           struct {
		Enabled          bool   `json:"enabled"`
		Interval         string `json:"interval"`
		Timeout          string `json:"timeout"`
		FailureThreshold *int   `json:"failure_threshold"`
		SuccessThreshold *int   `json:"success_threshold"`
		MaxConcurrency   *int   `json:"max_concurrency"`
	} `json:"tcp_reachability"`
}

func loadRuntimeConfig(path string) (runtimeConfig, error) {
	raw, err := readConfigFile(path)
	if err != nil {
		return runtimeConfig{}, err
	}
	var input fileConfig
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&input); err != nil {
		return runtimeConfig{}, fmt.Errorf("decode gateway configuration: %w", err)
	}
	if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		if err == nil {
			err = errors.New("contains multiple JSON values")
		}
		return runtimeConfig{}, fmt.Errorf("decode gateway configuration: %w", err)
	}

	configuration := runtimeConfig{
		ListenAddress:             strings.TrimSpace(input.ListenAddress),
		AllowNonLoopback:          input.AllowNonLoopback,
		SelectionPolicy:           endpointrouter.SelectionPolicy(strings.TrimSpace(input.SelectionPolicy)),
		MembershipFile:            strings.TrimSpace(input.MembershipFile),
		MembershipURL:             strings.TrimSpace(input.MembershipURL),
		MembershipTokenFile:       strings.TrimSpace(input.MembershipTokenFile),
		MembershipRefreshInterval: time.Second,
		DialTimeout:               10 * time.Second,
		TCPReachability: tcpReachabilityConfig{
			Enabled: input.TCPReachability.Enabled, Interval: 5 * time.Second, Timeout: time.Second,
			FailureThreshold: 3, SuccessThreshold: 2, MaxConcurrency: 32,
		},
	}
	if configuration.ListenAddress == "" {
		configuration.ListenAddress = "127.0.0.1:8443"
	}
	if configuration.SelectionPolicy == "" {
		configuration.SelectionPolicy = endpointrouter.SelectionPolicyWeightedRoundRobin
	}
	if (configuration.MembershipFile == "") == (configuration.MembershipURL == "") {
		return runtimeConfig{}, errors.New("configure exactly one of membership_file or membership_url")
	}
	if configuration.MembershipFile != "" {
		if !filepath.IsAbs(configuration.MembershipFile) || configuration.MembershipTokenFile != "" {
			return runtimeConfig{}, errors.New("membership_file must be absolute and cannot use membership_token_file")
		}
		configuration.MembershipFile = filepath.Clean(configuration.MembershipFile)
	} else if configuration.MembershipTokenFile == "" {
		return runtimeConfig{}, errors.New("membership_token_file is required with membership_url")
	}
	if input.MembershipRefreshInterval != "" {
		configuration.MembershipRefreshInterval, err = time.ParseDuration(input.MembershipRefreshInterval)
		if err != nil {
			return runtimeConfig{}, fmt.Errorf("parse membership_refresh_interval: %w", err)
		}
	}
	if configuration.MembershipRefreshInterval < 100*time.Millisecond || configuration.MembershipRefreshInterval > 5*time.Minute {
		return runtimeConfig{}, errors.New("membership_refresh_interval must be between 100ms and 5m")
	}
	if input.DialTimeout != "" {
		configuration.DialTimeout, err = time.ParseDuration(input.DialTimeout)
		if err != nil {
			return runtimeConfig{}, fmt.Errorf("parse dial_timeout: %w", err)
		}
	}
	if configuration.DialTimeout < 100*time.Millisecond || configuration.DialTimeout > 2*time.Minute {
		return runtimeConfig{}, errors.New("dial_timeout must be between 100ms and 2m")
	}
	if input.TCPReachability.Interval != "" {
		configuration.TCPReachability.Interval, err = time.ParseDuration(input.TCPReachability.Interval)
		if err != nil {
			return runtimeConfig{}, fmt.Errorf("parse tcp_reachability.interval: %w", err)
		}
	}
	if input.TCPReachability.Timeout != "" {
		configuration.TCPReachability.Timeout, err = time.ParseDuration(input.TCPReachability.Timeout)
		if err != nil {
			return runtimeConfig{}, fmt.Errorf("parse tcp_reachability.timeout: %w", err)
		}
	}
	if input.TCPReachability.FailureThreshold != nil {
		configuration.TCPReachability.FailureThreshold = *input.TCPReachability.FailureThreshold
	}
	if input.TCPReachability.SuccessThreshold != nil {
		configuration.TCPReachability.SuccessThreshold = *input.TCPReachability.SuccessThreshold
	}
	if input.TCPReachability.MaxConcurrency != nil {
		configuration.TCPReachability.MaxConcurrency = *input.TCPReachability.MaxConcurrency
	}
	if err := validateTCPReachability(configuration.TCPReachability); err != nil {
		return runtimeConfig{}, err
	}
	if err := validateListenAddress(configuration.ListenAddress, configuration.AllowNonLoopback); err != nil {
		return runtimeConfig{}, err
	}
	if _, err := endpointrouter.New(configuration.SelectionPolicy); err != nil {
		return runtimeConfig{}, err
	}
	return configuration, nil
}

func validateTCPReachability(configuration tcpReachabilityConfig) error {
	if configuration.Interval < 100*time.Millisecond || configuration.Interval > 5*time.Minute {
		return errors.New("tcp_reachability.interval must be between 100ms and 5m")
	}
	if configuration.Timeout < 10*time.Millisecond || configuration.Timeout > configuration.Interval {
		return errors.New("tcp_reachability.timeout must be between 10ms and interval")
	}
	if configuration.FailureThreshold < 1 || configuration.FailureThreshold > 1000 {
		return errors.New("tcp_reachability.failure_threshold must be between 1 and 1000")
	}
	if configuration.SuccessThreshold < 1 || configuration.SuccessThreshold > 1000 {
		return errors.New("tcp_reachability.success_threshold must be between 1 and 1000")
	}
	if configuration.MaxConcurrency < 1 || configuration.MaxConcurrency > 4096 {
		return errors.New("tcp_reachability.max_concurrency must be between 1 and 4096")
	}
	return nil
}

func readConfigFile(path string) ([]byte, error) {
	info, err := os.Lstat(path)
	if err != nil {
		return nil, fmt.Errorf("inspect gateway configuration: %w", err)
	}
	if !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 {
		return nil, errors.New("gateway configuration must be a regular file")
	}
	if info.Size() > maxGatewayConfigBytes {
		return nil, fmt.Errorf("gateway configuration exceeds %d bytes", maxGatewayConfigBytes)
	}
	file, err := os.Open(path)
	if err != nil {
		return nil, fmt.Errorf("open gateway configuration: %w", err)
	}
	defer file.Close()
	openedInfo, err := file.Stat()
	if err != nil {
		return nil, fmt.Errorf("inspect opened gateway configuration: %w", err)
	}
	if !openedInfo.Mode().IsRegular() || openedInfo.Size() > maxGatewayConfigBytes {
		return nil, errors.New("opened gateway configuration is not a bounded regular file")
	}
	raw, err := io.ReadAll(io.LimitReader(file, maxGatewayConfigBytes+1))
	if err != nil {
		return nil, fmt.Errorf("read gateway configuration: %w", err)
	}
	if int64(len(raw)) > maxGatewayConfigBytes {
		return nil, fmt.Errorf("gateway configuration exceeds %d bytes", maxGatewayConfigBytes)
	}
	return raw, nil
}

func validateListenAddress(address string, allowNonLoopback bool) error {
	host, portText, err := net.SplitHostPort(address)
	if err != nil {
		return fmt.Errorf("listen_address must be a host:port pair: %w", err)
	}
	port, err := strconv.Atoi(portText)
	if err != nil || port < 1 || port > 65535 {
		return errors.New("listen_address port must be between 1 and 65535")
	}
	if !allowNonLoopback && !isLoopbackHost(host) {
		return errors.New("refusing non-loopback listener; explicitly set allow_non_loopback=true after network and firewall review")
	}
	return nil
}

func isLoopbackHost(host string) bool {
	if strings.EqualFold(host, "localhost") {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}
