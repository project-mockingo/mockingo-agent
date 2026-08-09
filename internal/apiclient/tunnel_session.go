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

const TunnelProtocolVersion = 1

type TunnelSessionRequest struct {
	EndpointName    string `json:"endpointName"`
	Protocol        string `json:"protocol"`
	LocalPort       int    `json:"localPort"`
	ProtocolVersion int    `json:"protocolVersion"`
}

type EndpointResponse struct {
	ID        string `json:"id"`
	Name      string `json:"name"`
	Hostname  string `json:"hostname"`
	PublicURL string `json:"publicUrl"`
}

type TunnelResponse struct {
	SessionID       string    `json:"sessionId"`
	ConnectURL      string    `json:"connectUrl"`
	Ticket          string    `json:"ticket"`
	ExpiresAt       time.Time `json:"expiresAt"`
	ProtocolVersion int       `json:"protocolVersion"`
}

type TunnelSessionResponse struct {
	Endpoint EndpointResponse `json:"endpoint"`
	Tunnel   TunnelResponse   `json:"tunnel"`
}

func (r TunnelResponse) String() string {
	return fmt.Sprintf("{SessionID:%s ConnectURL:%s Ticket:<redacted> ExpiresAt:%s ProtocolVersion:%d}", r.SessionID, r.ConnectURL, r.ExpiresAt.Format(time.RFC3339), r.ProtocolVersion)
}

func (r TunnelSessionResponse) String() string {
	return fmt.Sprintf("{Endpoint:%+v Tunnel:%s}", r.Endpoint, r.Tunnel.String())
}

type Problem struct {
	Status    int               `json:"status"`
	Code      string            `json:"code"`
	Message   string            `json:"message"`
	Path      string            `json:"path,omitempty"`
	RequestID string            `json:"requestId,omitempty"`
	Fields    map[string]string `json:"fields,omitempty"`
}

type APIError struct {
	Problem Problem
}

func (e *APIError) Error() string {
	message := sanitizeErrorText(e.Problem.Message)
	if message == "" {
		message = "Mockingo API rejected the tunnel session request"
	}
	if safeRequestID(e.Problem.RequestID) {
		return fmt.Sprintf("%s (request ID: %s)", message, e.Problem.RequestID)
	}
	return message
}

func sanitizeErrorText(value string) string {
	value = strings.Join(strings.Fields(value), " ")
	runes := []rune(value)
	if len(runes) > 512 {
		value = string(runes[:512])
	}
	return value
}

func safeRequestID(value string) bool {
	if len(value) < 1 || len(value) > 64 {
		return false
	}
	for index, r := range value {
		if (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9') || (index > 0 && strings.ContainsRune("._:-", r)) {
			continue
		}
		return false
	}
	return true
}

func IsTemporarySessionConflict(err error) bool {
	var apiErr *APIError
	if !errors.As(err, &apiErr) || apiErr.Problem.Status != http.StatusConflict {
		return false
	}
	return apiErr.Problem.Code == "endpoint_already_connected" || apiErr.Problem.Code == "tunnel_session_pending"
}

func IsRetryable(err error) bool {
	if IsTemporarySessionConflict(err) {
		return true
	}
	var apiErr *APIError
	if errors.As(err, &apiErr) {
		return apiErr.Problem.Status == http.StatusTooManyRequests || apiErr.Problem.Status == http.StatusBadGateway || apiErr.Problem.Status == http.StatusServiceUnavailable || apiErr.Problem.Status == http.StatusGatewayTimeout
	}
	var netErr net.Error
	return errors.As(err, &netErr)
}

type TunnelSessionValidation struct {
	ExpectedGatewayHosts []string
	AllowInsecureLocal   bool
	Now                  func() time.Time
}

func (c *Client) CreateTunnelSession(ctx context.Context, request TunnelSessionRequest, validation TunnelSessionValidation) (TunnelSessionResponse, error) {
	payload, err := json.Marshal(request)
	if err != nil {
		return TunnelSessionResponse{}, fmt.Errorf("encode tunnel session request: %w", err)
	}
	response, err := c.doWithHeaders(ctx, http.MethodPost, "/api/v1/tunnel-sessions", payload, func(header http.Header) {
		header.Set("Content-Type", "application/json")
		header.Set("X-Request-ID", uuid.NewString())
	})
	if err != nil {
		return TunnelSessionResponse{}, fmt.Errorf("request tunnel session: %w", err)
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
			return TunnelSessionResponse{}, ErrSignedOut
		}
		return TunnelSessionResponse{}, &APIError{Problem: problem}
	}
	var result TunnelSessionResponse
	body, err := io.ReadAll(io.LimitReader(response.Body, maxResponseSize+1))
	if err != nil {
		return TunnelSessionResponse{}, fmt.Errorf("read tunnel session response: %w", err)
	}
	if len(body) > maxResponseSize {
		return TunnelSessionResponse{}, errors.New("tunnel session response is too large")
	}
	if err := json.Unmarshal(body, &result); err != nil {
		return TunnelSessionResponse{}, fmt.Errorf("decode tunnel session response: %w", err)
	}
	if err := ValidateTunnelSession(request, result, validation); err != nil {
		return TunnelSessionResponse{}, fmt.Errorf("invalid tunnel session response: %w", err)
	}
	return result, nil
}

func (c *Client) doWithHeaders(ctx context.Context, method, path string, body []byte, add func(http.Header)) (*http.Response, error) {
	credentials, err := c.credentials(ctx, false)
	if err != nil {
		return nil, err
	}
	for attempt := 0; attempt < 2; attempt++ {
		req, err := http.NewRequestWithContext(ctx, method, strings.TrimRight(c.APIURL, "/")+path, strings.NewReader(string(body)))
		if err != nil {
			return nil, err
		}
		req.Header.Set("Authorization", "Bearer "+credentials.AccessToken)
		req.Header.Set("Accept", "application/json")
		req.Header.Set("Cache-Control", "no-store")
		add(req.Header)
		httpClient := *c.HTTP
		httpClient.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
		response, err := httpClient.Do(req)
		if err != nil {
			return nil, err
		}
		if response.StatusCode != http.StatusUnauthorized || attempt == 1 {
			return response, nil
		}
		_, _ = io.Copy(io.Discard, io.LimitReader(response.Body, 4096))
		_ = response.Body.Close()
		credentials, err = c.credentials(ctx, true)
		if err != nil {
			return nil, err
		}
	}
	panic("unreachable")
}

func ValidateTunnelSession(request TunnelSessionRequest, response TunnelSessionResponse, validation TunnelSessionValidation) error {
	now := time.Now
	if validation.Now != nil {
		now = validation.Now
	}
	if request.Protocol != "http" || request.LocalPort < 1 || request.LocalPort > 65535 {
		return errors.New("unsupported local tunnel target")
	}
	if response.Endpoint.ID == "" {
		return errors.New("endpoint ID is missing")
	}
	if response.Endpoint.Name != request.EndpointName {
		return errors.New("endpoint name does not match the request")
	}
	if !validHostname(response.Endpoint.Hostname) || !strings.EqualFold(response.Endpoint.Hostname, request.EndpointName+".mockingo.click") {
		return errors.New("endpoint hostname is not a valid mockingo.click hostname")
	}
	publicURL, err := url.ParseRequestURI(response.Endpoint.PublicURL)
	if err != nil || publicURL.Scheme != "https" || publicURL.User != nil || publicURL.Port() != "" || publicURL.RawQuery != "" || publicURL.Fragment != "" || !strings.EqualFold(publicURL.Hostname(), response.Endpoint.Hostname) {
		return errors.New("public URL must be HTTPS and match the endpoint hostname")
	}
	if response.Tunnel.SessionID == "" {
		return errors.New("session ID is missing")
	}
	if response.Tunnel.Ticket == "" {
		return errors.New("tunnel ticket is missing")
	}
	if !response.Tunnel.ExpiresAt.After(now()) {
		return errors.New("tunnel ticket is expired")
	}
	if request.ProtocolVersion != TunnelProtocolVersion || response.Tunnel.ProtocolVersion != TunnelProtocolVersion || response.Tunnel.ProtocolVersion != request.ProtocolVersion {
		return errors.New("unsupported tunnel protocol version")
	}
	connectURL, err := url.ParseRequestURI(response.Tunnel.ConnectURL)
	if err != nil || connectURL.Host == "" || connectURL.User != nil || connectURL.Fragment != "" || connectURL.RawQuery != "" || connectURL.Path != "/v1/connect" {
		return errors.New("gateway connect URL is invalid")
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
		return errors.New("gateway connect URL host is not trusted")
	}
	if connectURL.Scheme != "wss" {
		local := host == "localhost" || host == "127.0.0.1" || host == "::1"
		if !validation.AllowInsecureLocal || !local || connectURL.Scheme != "ws" {
			return errors.New("gateway connect URL must use wss")
		}
	}
	if net.ParseIP(host) != nil && !validation.AllowInsecureLocal {
		return errors.New("gateway connect URL must not use an IP address")
	}
	return nil
}

func validHostname(value string) bool {
	if len(value) == 0 || len(value) > 253 || strings.HasPrefix(value, ".") || strings.HasSuffix(value, ".") || net.ParseIP(value) != nil {
		return false
	}
	for _, label := range strings.Split(value, ".") {
		if len(label) == 0 || len(label) > 63 || label[0] == '-' || label[len(label)-1] == '-' {
			return false
		}
		for _, r := range label {
			if (r < 'a' || r > 'z') && (r < 'A' || r > 'Z') && (r < '0' || r > '9') && r != '-' {
				return false
			}
		}
	}
	return true
}
