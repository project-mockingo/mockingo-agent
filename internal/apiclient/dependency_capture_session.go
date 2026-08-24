package apiclient

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/google/uuid"
)

type DependencyCaptureSessionResponse struct {
	Endpoint struct {
		ID   string `json:"id"`
		Name string `json:"name"`
	} `json:"endpoint"`
	Capture struct {
		SessionID       string    `json:"sessionId"`
		ConnectURL      string    `json:"connectUrl"`
		Ticket          string    `json:"ticket"`
		ExpiresAt       time.Time `json:"expiresAt"`
		ProtocolVersion int       `json:"protocolVersion"`
	} `json:"capture"`
}

func (c *Client) CreateDependencyCaptureSession(ctx context.Context, endpointName string, validation TunnelSessionValidation) (DependencyCaptureSessionResponse, error) {
	path := "/api/v1/endpoints/" + url.PathEscape(endpointName) + "/dependency-capture-sessions"
	response, err := c.doWithHeaders(ctx, http.MethodPost, path, nil, func(header http.Header) {
		header.Set("X-Request-ID", uuid.NewString())
	})
	if err != nil {
		return DependencyCaptureSessionResponse{}, fmt.Errorf("request dependency capture session: %w", err)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusCreated {
		problem := Problem{Status: response.StatusCode}
		_ = json.NewDecoder(io.LimitReader(response.Body, maxResponseSize)).Decode(&problem)
		problem.Status = response.StatusCode
		if headerRequestID := response.Header.Get("X-Request-ID"); safeRequestID(headerRequestID) {
			problem.RequestID = headerRequestID
		}
		if response.StatusCode == http.StatusUnauthorized {
			return DependencyCaptureSessionResponse{}, ErrSignedOut
		}
		return DependencyCaptureSessionResponse{}, &APIError{Problem: problem}
	}
	var result DependencyCaptureSessionResponse
	body, err := io.ReadAll(io.LimitReader(response.Body, maxResponseSize+1))
	if err != nil || len(body) > maxResponseSize {
		return DependencyCaptureSessionResponse{}, errors.New("dependency capture session response is invalid or too large")
	}
	if err := json.Unmarshal(body, &result); err != nil {
		return DependencyCaptureSessionResponse{}, fmt.Errorf("decode dependency capture session response: %w", err)
	}
	if err := validateDependencyCaptureSession(endpointName, result, validation); err != nil {
		return DependencyCaptureSessionResponse{}, fmt.Errorf("invalid dependency capture session response: %w", err)
	}
	return result, nil
}

func validateDependencyCaptureSession(endpointName string, response DependencyCaptureSessionResponse, validation TunnelSessionValidation) error {
	now := time.Now
	if validation.Now != nil {
		now = validation.Now
	}
	if uuid.Validate(response.Endpoint.ID) != nil || response.Endpoint.Name != endpointName || uuid.Validate(response.Capture.SessionID) != nil || response.Capture.Ticket == "" || !response.Capture.ExpiresAt.After(now()) || response.Capture.ProtocolVersion != 1 {
		return errors.New("capture session identity is invalid")
	}
	connectURL, err := url.ParseRequestURI(response.Capture.ConnectURL)
	if err != nil || connectURL.Host == "" || connectURL.User != nil || connectURL.Fragment != "" || connectURL.RawQuery != "" || connectURL.Path != "/v1/dependency-capture/connect" {
		return errors.New("gateway capture URL is invalid")
	}
	host := strings.ToLower(connectURL.Hostname())
	trusted := false
	for _, allowed := range validation.ExpectedGatewayHosts {
		if strings.EqualFold(strings.TrimSpace(allowed), host) {
			trusted = true
			break
		}
	}
	if !trusted {
		return errors.New("gateway capture URL host is not trusted")
	}
	if connectURL.Scheme != "wss" {
		local := host == "localhost" || host == "127.0.0.1" || host == "::1"
		if !validation.AllowInsecureLocal || !local || connectURL.Scheme != "ws" {
			return errors.New("gateway capture URL must use wss")
		}
	}
	if net.ParseIP(host) != nil && !validation.AllowInsecureLocal {
		return errors.New("gateway capture URL must not use an IP address")
	}
	return nil
}
