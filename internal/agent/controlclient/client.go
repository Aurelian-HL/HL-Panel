package controlclient

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"

	"github.com/hongle/hl-panel/internal/protocol/agentv1"
)

const (
	apiPrefix               = "/api/v1"
	maxOrdinaryResponseBody = 64 << 10
	maxDesiredResponseBody  = (4 << 20) + (64 << 10)
	maxErrorBody            = 512
)

type HTTPError struct {
	StatusCode int
	Message    string
}

func (e *HTTPError) Error() string {
	if e.Message == "" {
		return fmt.Sprintf("control plane returned HTTP %d", e.StatusCode)
	}
	return fmt.Sprintf("control plane returned HTTP %d: %s", e.StatusCode, e.Message)
}

type Client struct {
	baseURL    *url.URL
	httpClient *http.Client
}

func New(controlPlaneURL string, httpClient *http.Client) (*Client, error) {
	return NewWithOptions(controlPlaneURL, httpClient, false)
}

func NewWithOptions(controlPlaneURL string, httpClient *http.Client, allowInsecureLoopback bool) (*Client, error) {
	baseURL, err := url.Parse(controlPlaneURL)
	if err != nil {
		return nil, fmt.Errorf("parse control plane URL: %w", err)
	}
	if baseURL.Host == "" || baseURL.User != nil || baseURL.RawQuery != "" || baseURL.Fragment != "" {
		return nil, errors.New("control plane URL must be an origin without credentials, query, or fragment")
	}
	if baseURL.Scheme != "https" && !(baseURL.Scheme == "http" && allowInsecureLoopback && isLoopbackHost(baseURL.Hostname())) {
		return nil, errors.New("control plane URL must use HTTPS unless explicit loopback HTTP is enabled")
	}
	if baseURL.Path != "" && baseURL.Path != "/" {
		return nil, errors.New("control plane URL must not contain a path")
	}
	if httpClient == nil {
		return nil, errors.New("HTTP client is required")
	}
	baseURL.Path = ""
	return &Client{baseURL: baseURL, httpClient: httpClient}, nil
}

func isLoopbackHost(host string) bool {
	switch strings.ToLower(strings.Trim(host, "[]")) {
	case "localhost", "127.0.0.1", "::1":
		return true
	default:
		return false
	}
}

func (c *Client) Enroll(ctx context.Context, request agentv1.EnrollmentRequest) (agentv1.EnrollmentResponse, error) {
	body, status, err := c.do(ctx, http.MethodPost, "/agent/enroll", "", request, maxOrdinaryResponseBody)
	if err != nil {
		return agentv1.EnrollmentResponse{}, err
	}
	if status != http.StatusOK && status != http.StatusCreated {
		return agentv1.EnrollmentResponse{}, httpError(status, body)
	}
	var response agentv1.EnrollmentResponse
	if err := decodeStrict(body, &response); err != nil {
		return agentv1.EnrollmentResponse{}, fmt.Errorf("decode enrollment response: %w", err)
	}
	if err := validateCredential(response.NodeID, "node_id"); err != nil {
		return agentv1.EnrollmentResponse{}, err
	}
	if err := validateCredential(response.NodeCredential, "node_credential"); err != nil {
		return agentv1.EnrollmentResponse{}, err
	}
	return response, nil
}

func (c *Client) Heartbeat(ctx context.Context, nodeCredential string, request agentv1.HeartbeatRequest) error {
	body, status, err := c.do(ctx, http.MethodPost, "/agent/heartbeat", nodeCredential, request, maxOrdinaryResponseBody)
	if err != nil {
		return err
	}
	if status != http.StatusOK && status != http.StatusNoContent {
		return httpError(status, body)
	}
	return nil
}

func (c *Client) Desired(ctx context.Context, nodeCredential string) (*agentv1.DesiredNodeConfig, error) {
	body, status, err := c.do(ctx, http.MethodGet, "/agent/desired", nodeCredential, nil, maxDesiredResponseBody)
	if err != nil {
		return nil, err
	}
	if status == http.StatusNoContent {
		return nil, nil
	}
	if status != http.StatusOK {
		return nil, httpError(status, body)
	}
	var desired agentv1.DesiredNodeConfig
	if err := decodeStrict(body, &desired); err != nil {
		return nil, fmt.Errorf("decode desired node configuration: %w", err)
	}
	return &desired, nil
}

func (c *Client) ReportApplyResult(ctx context.Context, nodeCredential string, request agentv1.ApplyResultRequest) error {
	body, status, err := c.do(ctx, http.MethodPost, "/agent/apply-results", nodeCredential, request, maxOrdinaryResponseBody)
	if err != nil {
		return err
	}
	if status != http.StatusOK && status != http.StatusNoContent && status != http.StatusAccepted {
		return httpError(status, body)
	}
	return nil
}

func (c *Client) ReportUsage(ctx context.Context, nodeCredential string, request agentv1.UsageReport) (agentv1.UsageAcknowledgement, error) {
	body, status, err := c.do(ctx, http.MethodPost, "/usage/reports", nodeCredential, request, maxOrdinaryResponseBody)
	if err != nil {
		return agentv1.UsageAcknowledgement{}, err
	}
	if status != http.StatusOK && status != http.StatusCreated {
		return agentv1.UsageAcknowledgement{}, httpError(status, body)
	}
	var response struct {
		Acknowledgement agentv1.UsageAcknowledgement `json:"acknowledgement"`
	}
	if err := decodeStrict(body, &response); err != nil {
		return agentv1.UsageAcknowledgement{}, fmt.Errorf("decode usage acknowledgement: %w", err)
	}
	acknowledgement := response.Acknowledgement
	if acknowledgement.NodeID != request.NodeID || acknowledgement.BootID != request.BootID || acknowledgement.Sequence != request.Sequence {
		return agentv1.UsageAcknowledgement{}, errors.New("usage acknowledgement does not match submitted report")
	}
	return acknowledgement, nil
}

func (c *Client) DesiredEnforcement(ctx context.Context, nodeCredential string) (*agentv1.EnforcementCommand, error) {
	body, status, err := c.do(ctx, http.MethodGet, "/usage/enforcement/desired", nodeCredential, nil, maxOrdinaryResponseBody)
	if err != nil {
		return nil, err
	}
	if status == http.StatusNoContent {
		return nil, nil
	}
	if status != http.StatusOK {
		return nil, httpError(status, body)
	}
	var command agentv1.EnforcementCommand
	if err := decodeStrict(body, &command); err != nil {
		return nil, fmt.Errorf("decode enforcement command: %w", err)
	}
	if command.DecisionID == "" || command.CustomerID == "" || command.Revision <= 0 ||
		(command.Action != agentv1.EnforcementDisableCustomer && command.Action != agentv1.EnforcementEnableCustomer) {
		return nil, errors.New("control plane returned an invalid enforcement command")
	}
	return &command, nil
}

func (c *Client) ReportEnforcementResult(ctx context.Context, nodeCredential string, request agentv1.EnforcementResultRequest) error {
	body, status, err := c.do(ctx, http.MethodPost, "/usage/enforcement/results", nodeCredential, request, maxOrdinaryResponseBody)
	if err != nil {
		return err
	}
	if status != http.StatusOK && status != http.StatusNoContent {
		return httpError(status, body)
	}
	return nil
}

func (c *Client) do(ctx context.Context, method, path, nodeCredential string, payload any, maximumResponse int64) ([]byte, int, error) {
	var requestBody io.Reader
	if payload != nil {
		encoded, err := json.Marshal(payload)
		if err != nil {
			return nil, 0, fmt.Errorf("encode control plane request: %w", err)
		}
		requestBody = bytes.NewReader(encoded)
	}
	endpoint := *c.baseURL
	endpoint.Path = apiPrefix + path
	request, err := http.NewRequestWithContext(ctx, method, endpoint.String(), requestBody)
	if err != nil {
		return nil, 0, err
	}
	request.Header.Set("Accept", "application/json")
	if payload != nil {
		request.Header.Set("Content-Type", "application/json")
	}
	if nodeCredential != "" {
		request.Header.Set("Authorization", "Bearer "+nodeCredential)
	} else if path != "/agent/enroll" {
		return nil, 0, errors.New("node credential is required")
	}
	response, err := c.httpClient.Do(request)
	if err != nil {
		return nil, 0, fmt.Errorf("control plane request failed: %w", err)
	}
	defer response.Body.Close()
	body, err := io.ReadAll(io.LimitReader(response.Body, maximumResponse+1))
	if err != nil {
		return nil, 0, fmt.Errorf("read control plane response: %w", err)
	}
	if int64(len(body)) > maximumResponse {
		return nil, 0, fmt.Errorf("control plane response exceeds %d bytes", maximumResponse)
	}
	return body, response.StatusCode, nil
}

func httpError(status int, body []byte) error {
	if len(body) > maxErrorBody {
		body = body[:maxErrorBody]
	}
	message := strings.Map(func(character rune) rune {
		if character < 0x20 || character == 0x7f {
			return ' '
		}
		return character
	}, string(body))
	message = strings.Join(strings.Fields(message), " ")
	message = redactSecrets(message)
	return &HTTPError{StatusCode: status, Message: message}
}

func validateCredential(value, name string) error {
	if strings.TrimSpace(value) == "" {
		return fmt.Errorf("enrollment response has an empty %s", name)
	}
	if strings.TrimSpace(value) != value || strings.ContainsAny(value, "\r\n\t") {
		return fmt.Errorf("enrollment response has invalid whitespace in %s", name)
	}
	if len(value) > 4096 {
		return fmt.Errorf("enrollment response %s exceeds size limit", name)
	}
	return nil
}

func redactSecrets(message string) string {
	parts := strings.Fields(message)
	for index := 0; index < len(parts); index++ {
		lower := strings.ToLower(parts[index])
		if lower == "bearer" && index+1 < len(parts) {
			parts[index+1] = "[redacted]"
			index++
			continue
		}
		for _, prefix := range []string{"token=", "credential=", "password="} {
			if strings.HasPrefix(lower, prefix) {
				parts[index] = parts[index][:len(prefix)] + "[redacted]"
				break
			}
		}
	}
	return strings.Join(parts, " ")
}

func decodeStrict(body []byte, target any) error {
	if len(body) == 0 {
		return errors.New("response body is empty")
	}
	decoder := json.NewDecoder(bytes.NewReader(body))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil {
		return err
	}
	if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		if err == nil {
			return errors.New("response contains multiple JSON values")
		}
		return err
	}
	return nil
}
