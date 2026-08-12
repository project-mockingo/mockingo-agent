package tunnelprotocol

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
)

// Encode validates and marshals a protocol v1 message using encoding/json.
func Encode(message Message) ([]byte, error) {
	if err := Validate(message); err != nil {
		return nil, err
	}
	return json.Marshal(message)
}

// Decode unmarshals one bounded protocol v1 JSON message. Unknown JSON fields
// remain accepted to preserve encoding/json compatibility.
func Decode(data []byte) (Message, error) {
	if len(data) > MaxMessageSize {
		return Message{}, ErrMessageTooLarge
	}
	decoder := json.NewDecoder(bytes.NewReader(data))
	var message Message
	if err := decoder.Decode(&message); err != nil {
		return Message{}, errors.Join(ErrInvalidMessage, err)
	}
	if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		return Message{}, ErrInvalidMessage
	}
	if err := Validate(message); err != nil {
		return Message{}, err
	}
	return message, nil
}
