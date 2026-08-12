package tunnelprotocol

import "errors"

var (
	ErrBodyTooLarge       = errors.New("body exceeds 10 MiB limit")
	ErrMessageTooLarge    = errors.New("protocol message exceeds the maximum size")
	ErrInvalidMessage     = errors.New("invalid protocol message")
	ErrUnknownMessageType = errors.New("unknown protocol message type")
	ErrUnsupportedVersion = errors.New("unsupported protocol version")
)
