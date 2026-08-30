package tunnelprotocol

import "time"

const (
	TypeRequest               = "request"
	TypeResponse              = "response"
	TypePing                  = "ping"
	TypePong                  = "pong"
	TypeError                 = "error"
	TypeDependencyInteraction = "dependency_interaction_completed"
	TypeDependencyConfig      = "dependency_behavior_snapshot"
	TypeTCPOpen               = "tcp_open"
	TypeTCPOpened             = "tcp_opened"
	TypeTCPData               = "tcp_data"
	TypeTCPHalfClose          = "tcp_half_close"
	TypeTCPClose              = "tcp_close"
	TypeTCPError              = "tcp_error"
	TypeTCPConnectionEvent    = "tcp_connection_event"
	TypeTCPDependencyConfig   = "tcp_dependency_snapshot"
)

// Message is the complete protocol v1 JSON envelope. Field order and JSON
// tags are compatibility-sensitive.
type Message struct {
	Version          int                         `json:"version"`
	Type             string                      `json:"type"`
	RequestID        string                      `json:"requestId,omitempty"`
	Method           string                      `json:"method,omitempty"`
	Path             string                      `json:"path,omitempty"`
	Headers          map[string][]string         `json:"headers,omitempty"`
	BodyBase64       string                      `json:"bodyBase64,omitempty"`
	Status           int                         `json:"status,omitempty"`
	ErrorCode        string                      `json:"errorCode,omitempty"`
	Error            string                      `json:"error,omitempty"`
	Dependency       *DependencyInteraction      `json:"dependency,omitempty"`
	DependencyConfig *DependencyBehaviorSnapshot `json:"dependencyConfig,omitempty"`
	ConnectionID     string                      `json:"connectionId,omitempty"`
	DataBase64       string                      `json:"dataBase64,omitempty"`
	TCPConnection    *TCPConnectionEvent         `json:"tcpConnection,omitempty"`
	TCPDependencies  *TCPDependencySnapshot      `json:"tcpDependencies,omitempty"`
}

type TCPDependencySnapshot struct {
	EndpointID   string          `json:"endpointId"`
	Dependencies []TCPDependency `json:"dependencies"`
}

type TCPDependency struct {
	ID         string `json:"id"`
	Name       string `json:"name"`
	ListenHost string `json:"listenHost"`
	ListenPort int    `json:"listenPort"`
	TargetHost string `json:"targetHost"`
	TargetPort int    `json:"targetPort"`
}

// TCPConnectionEvent is metadata-only. Payload bytes are never retained in Traffic.
type TCPConnectionEvent struct {
	ID                  string    `json:"id"`
	DependencyID        string    `json:"dependencyId,omitempty"`
	TrafficType         string    `json:"trafficType"`
	State               string    `json:"state"`
	StartedAt           time.Time `json:"startedAt"`
	EndedAt             time.Time `json:"endedAt,omitempty"`
	LastActivityAt      time.Time `json:"lastActivityAt,omitempty"`
	DurationMS          int64     `json:"durationMs"`
	SourceHost          string    `json:"sourceHost,omitempty"`
	SourcePort          int       `json:"sourcePort,omitempty"`
	TargetHost          string    `json:"targetHost,omitempty"`
	TargetPort          int       `json:"targetPort,omitempty"`
	ListenHost          string    `json:"listenHost,omitempty"`
	ListenPort          int       `json:"listenPort,omitempty"`
	PublicHost          string    `json:"publicHost,omitempty"`
	PublicPort          int       `json:"publicPort,omitempty"`
	BytesClientToServer int64     `json:"bytesClientToServer"`
	BytesServerToClient int64     `json:"bytesServerToClient"`
	CloseReason         string    `json:"closeReason,omitempty"`
	Error               string    `json:"error,omitempty"`
}

type DependencyBehaviorSnapshot struct {
	EndpointID string               `json:"endpointId"`
	Behaviors  []DependencyBehavior `json:"behaviors"`
}

type DependencyBehavior struct {
	ID      string              `json:"id"`
	Scheme  string              `json:"scheme"`
	Host    string              `json:"host"`
	Port    int                 `json:"port"`
	Method  string              `json:"method"`
	Path    string              `json:"path"`
	Status  int                 `json:"status"`
	Headers map[string][]string `json:"headers"`
	Body    string              `json:"body"`
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
	HandledBy   string           `json:"handledBy"`
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
