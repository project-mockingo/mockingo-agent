package tunnelprotocol

import (
	"errors"
	"io"
	"net"
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/google/uuid"
)

// Validate performs structural validation without applying gateway or local
// forwarding policy.
func Validate(message Message) error {
	if message.Version != Version {
		return ErrUnsupportedVersion
	}
	switch message.Type {
	case TypeRequest:
		if message.RequestID == "" || message.Method == "" || message.Path == "" {
			return ErrInvalidMessage
		}
	case TypeResponse:
		if message.RequestID == "" || message.Status == 0 {
			return ErrInvalidMessage
		}
	case TypeError:
		if message.RequestID == "" || message.ErrorCode == "" {
			return ErrInvalidMessage
		}
	case TypePing, TypePong:
		// Heartbeats have no required fields beyond the envelope.
	case TypeDependencyInteraction:
		if message.Dependency == nil || validateDependency(*message.Dependency) != nil {
			return ErrInvalidMessage
		}
	case TypeDependencyConfig:
		if message.DependencyConfig == nil || validateDependencyConfig(*message.DependencyConfig) != nil {
			return ErrInvalidMessage
		}
	default:
		return ErrUnknownMessageType
	}
	return nil
}

func validateDependency(value DependencyInteraction) error {
	if value.ID == "" || (value.Scheme != "http" && value.Scheme != "https") ||
		!validDependencyHost(value.Host) ||
		value.Port < 1 || value.Port > 65535 || value.StartedAt.IsZero() || value.CompletedAt.IsZero() ||
		(value.HandledBy != "" && value.HandledBy != "ORIGIN" && value.HandledBy != "REPLAY") ||
		value.CompletedAt.Before(value.StartedAt) || value.DurationMS < 0 ||
		value.Request.Method == "" || len(value.Request.Method) > 32 ||
		value.Request.Path == "" || len(value.Request.Path) > 8192 || value.Request.Path[0] != '/' ||
		len(value.Request.RawQuery) > 65536 || value.Request.SizeBytes < 0 ||
		value.Response.Status < 100 || value.Response.Status > 599 || value.Response.SizeBytes < 0 ||
		validateHeaders(value.Request.Headers) != nil || validateHeaders(value.Response.Headers) != nil ||
		validateCapturedBody(value.Request.Body, MaxDependencyRequestPreview) != nil ||
		validateCapturedBody(value.Response.Body, MaxDependencyResponsePreview) != nil {
		return ErrInvalidMessage
	}
	return nil
}

func validateDependencyConfig(value DependencyBehaviorSnapshot) error {
	if uuid.Validate(value.EndpointID) != nil || value.Behaviors == nil || len(value.Behaviors) > MaxDependencyBehaviors {
		return ErrInvalidMessage
	}
	seen := make(map[string]struct{}, len(value.Behaviors))
	for _, behavior := range value.Behaviors {
		if uuid.Validate(behavior.ID) != nil ||
			(behavior.Scheme != "http" && behavior.Scheme != "https") ||
			!validDependencyHost(behavior.Host) || behavior.Host != strings.ToLower(behavior.Host) ||
			behavior.Port < 1 || behavior.Port > 65535 || len(behavior.Method) > 32 || !validHTTPToken(behavior.Method) ||
			behavior.Method != strings.ToUpper(behavior.Method) || !validExactDependencyPath(behavior.Path) ||
			behavior.Status < 100 || behavior.Status > 599 || len(behavior.Body) > MaxDependencyReplayBody ||
			validateHeaders(behavior.Headers) != nil {
			return ErrInvalidMessage
		}
		key := behavior.Scheme + "\x00" + behavior.Host + "\x00" + strconv.Itoa(behavior.Port) + "\x00" + behavior.Method + "\x00" + behavior.Path
		if _, duplicate := seen[key]; duplicate {
			return ErrInvalidMessage
		}
		seen[key] = struct{}{}
	}
	return nil
}

func validExactDependencyPath(path string) bool {
	if path == "" || len(path) > 8192 || path[0] != '/' || strings.ContainsAny(path, "?#") {
		return false
	}
	for _, character := range path {
		if character <= 31 || character == 127 {
			return false
		}
	}
	return true
}

func validDependencyHost(host string) bool {
	if host == "" || len(host) > 253 || strings.ContainsAny(host, "/\\@ \t\r\n") {
		return false
	}
	if net.ParseIP(host) != nil {
		return true
	}
	for _, label := range strings.Split(host, ".") {
		if len(label) == 0 || len(label) > 63 || label[0] == '-' || label[len(label)-1] == '-' {
			return false
		}
		for _, character := range label {
			if (character < 'a' || character > 'z') && (character < 'A' || character > 'Z') && (character < '0' || character > '9') && character != '-' {
				return false
			}
		}
	}
	return true
}

func validateHeaders(headers map[string][]string) error {
	if headers == nil {
		return ErrInvalidMessage
	}
	size := 0
	for name, values := range headers {
		if !validHTTPToken(name) || values == nil {
			return ErrInvalidMessage
		}
		size += len(name)
		for _, value := range values {
			if strings.ContainsAny(value, "\r\n") {
				return ErrInvalidMessage
			}
			size += len(value)
		}
		if size > MaxDependencyHeaderBytes {
			return ErrMessageTooLarge
		}
	}
	return nil
}

func validHTTPToken(value string) bool {
	if value == "" {
		return false
	}
	for _, character := range []byte(value) {
		if !((character >= 'a' && character <= 'z') ||
			(character >= 'A' && character <= 'Z') ||
			(character >= '0' && character <= '9') ||
			strings.ContainsRune("!#$%&'*+-.^_`|~", rune(character))) {
			return false
		}
	}
	return true
}

func validateCapturedBody(body CapturedBody, limit int) error {
	if body.SizeBytes < 0 || body.CapturedBytes < 0 || body.CapturedBytes > int64(limit) || len(body.ContentType) > 255 || len(body.Reason) > 64 {
		return ErrInvalidMessage
	}
	if !body.Captured {
		if body.Content != "" || body.CapturedBytes != 0 || body.Truncated {
			return ErrInvalidMessage
		}
		return nil
	}
	if !utf8.ValidString(body.Content) || int64(len(body.Content)) != body.CapturedBytes ||
		body.CapturedBytes > body.SizeBytes || body.Truncated != (body.CapturedBytes < body.SizeBytes) {
		return ErrInvalidMessage
	}
	return nil
}

// ReadBody reads a body using the protocol v1 decoded body limit.
func ReadBody(reader io.Reader) ([]byte, error) {
	limited := io.LimitReader(reader, MaxBodySize+1)
	body, err := io.ReadAll(limited)
	if err != nil {
		return nil, err
	}
	if len(body) > MaxBodySize {
		return nil, ErrBodyTooLarge
	}
	return body, nil
}

// IsValidationError reports whether err is one of the stable structural
// validation categories returned by Decode or Validate.
func IsValidationError(err error) bool {
	return errors.Is(err, ErrInvalidMessage) || errors.Is(err, ErrUnknownMessageType) ||
		errors.Is(err, ErrUnsupportedVersion) || errors.Is(err, ErrMessageTooLarge)
}
