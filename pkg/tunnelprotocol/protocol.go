// Package tunnelprotocol defines the versioned messages exchanged by the
// Mockingo gateway and local agent.
package tunnelprotocol

import (
	"errors"
	"io"
	"net/http"
	"strings"
)

const (
	Version     = 1
	MaxBodySize = 10 << 20 // 10 MiB
	// Base64 and the JSON envelope add overhead to the buffered body.
	MaxMessageSize = MaxBodySize*4/3 + 1<<20

	TypeRequest  = "request"
	TypeResponse = "response"
	TypePing     = "ping"
	TypePong     = "pong"
	TypeError    = "error"
)

var ErrBodyTooLarge = errors.New("body exceeds 10 MiB limit")

type Message struct {
	Version    int                 `json:"version"`
	Type       string              `json:"type"`
	RequestID  string              `json:"requestId,omitempty"`
	Method     string              `json:"method,omitempty"`
	Path       string              `json:"path,omitempty"`
	Headers    map[string][]string `json:"headers,omitempty"`
	BodyBase64 string              `json:"bodyBase64,omitempty"`
	Status     int                 `json:"status,omitempty"`
	ErrorCode  string              `json:"errorCode,omitempty"`
	Error      string              `json:"error,omitempty"`
}

// ReadBody reads a bounded, buffered protocol body. Streaming is deliberately
// outside protocol version 1.
func ReadBody(r io.Reader) ([]byte, error) {
	limited := io.LimitReader(r, MaxBodySize+1)
	body, err := io.ReadAll(limited)
	if err != nil {
		return nil, err
	}
	if len(body) > MaxBodySize {
		return nil, ErrBodyTooLarge
	}
	return body, nil
}

var hopByHop = map[string]struct{}{
	"connection":          {},
	"keep-alive":          {},
	"proxy-authenticate":  {},
	"proxy-authorization": {},
	"te":                  {},
	"trailer":             {},
	"transfer-encoding":   {},
	"upgrade":             {},
}

// FilterHeaders returns a copy without RFC hop-by-hop headers, including
// headers nominated by the Connection header.
func FilterHeaders(src http.Header) http.Header {
	blocked := make(map[string]struct{}, len(hopByHop))
	for key := range hopByHop {
		blocked[key] = struct{}{}
	}
	for _, value := range src.Values("Connection") {
		for _, key := range strings.Split(value, ",") {
			blocked[strings.ToLower(strings.TrimSpace(key))] = struct{}{}
		}
	}
	out := make(http.Header)
	for key, values := range src {
		if _, found := blocked[strings.ToLower(key)]; found {
			continue
		}
		out[key] = append([]string(nil), values...)
	}
	return out
}
