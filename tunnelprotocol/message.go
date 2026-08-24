package tunnelprotocol

import "time"

const (
	TypeRequest               = "request"
	TypeResponse              = "response"
	TypePing                  = "ping"
	TypePong                  = "pong"
	TypeError                 = "error"
	TypeDependencyInteraction = "dependency_interaction_completed"
)

// Message is the complete protocol v1 JSON envelope. Field order and JSON
// tags are compatibility-sensitive.
type Message struct {
	Version    int                    `json:"version"`
	Type       string                 `json:"type"`
	RequestID  string                 `json:"requestId,omitempty"`
	Method     string                 `json:"method,omitempty"`
	Path       string                 `json:"path,omitempty"`
	Headers    map[string][]string    `json:"headers,omitempty"`
	BodyBase64 string                 `json:"bodyBase64,omitempty"`
	Status     int                    `json:"status,omitempty"`
	ErrorCode  string                 `json:"errorCode,omitempty"`
	Error      string                 `json:"error,omitempty"`
	Dependency *DependencyInteraction `json:"dependency,omitempty"`
}

// CapturedBody is a bounded, textual inspection copy. SizeBytes always refers
// to the complete body that continued through the local proxy.
type CapturedBody struct {
	Captured      bool   `json:"captured"`
	ContentType   string `json:"contentType,omitempty"`
	Content       string `json:"content,omitempty"`
	SizeBytes     int64  `json:"sizeBytes"`
	CapturedBytes int64  `json:"capturedBytes"`
	Truncated     bool   `json:"truncated"`
	Reason        string `json:"reason,omitempty"`
}

type CapturedRequest struct {
	Method    string              `json:"method"`
	Path      string              `json:"path"`
	RawQuery  string              `json:"rawQuery,omitempty"`
	Headers   map[string][]string `json:"headers"`
	Body      CapturedBody        `json:"body"`
	SizeBytes int64               `json:"sizeBytes"`
}

type CapturedResponse struct {
	Status    int                 `json:"status"`
	Headers   map[string][]string `json:"headers"`
	Body      CapturedBody        `json:"body"`
	SizeBytes int64               `json:"sizeBytes"`
}

// DependencyInteraction contains sanitized telemetry only. Upstream network
// traffic and local TLS material never travel in this message.
type DependencyInteraction struct {
	ID          string           `json:"id"`
	Scheme      string           `json:"scheme"`
	Host        string           `json:"host"`
	Port        int              `json:"port"`
	StartedAt   time.Time        `json:"startedAt"`
	CompletedAt time.Time        `json:"completedAt"`
	DurationMS  int64            `json:"durationMs"`
	Request     CapturedRequest  `json:"request"`
	Response    CapturedResponse `json:"response"`
}

// Protocol v1 error codes currently sent by the local agent.
const (
	ErrorCodeInvalidRequest     = "invalid_request"
	ErrorCodeTimeout            = "timeout"
	ErrorCodeLocalUnreachable   = "local_unreachable"
	ErrorCodeResponseTooLarge   = "response_too_large"
	ErrorCodeLocalResponseError = "local_response_error"
)
