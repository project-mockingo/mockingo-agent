package tunnelprotocol

import (
	"errors"
	"io"
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
	default:
		return ErrUnknownMessageType
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
