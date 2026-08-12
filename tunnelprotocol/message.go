package tunnelprotocol

const (
	TypeRequest  = "request"
	TypeResponse = "response"
	TypePing     = "ping"
	TypePong     = "pong"
	TypeError    = "error"
)

// Message is the complete protocol v1 JSON envelope. Field order and JSON
// tags are compatibility-sensitive.
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

// Protocol v1 error codes currently sent by the local agent.
const (
	ErrorCodeInvalidRequest     = "invalid_request"
	ErrorCodeTimeout            = "timeout"
	ErrorCodeLocalUnreachable   = "local_unreachable"
	ErrorCodeResponseTooLarge   = "response_too_large"
	ErrorCodeLocalResponseError = "local_response_error"
)
