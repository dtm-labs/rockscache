package rockscache

import (
	"encoding/json"
)

type (
	// Codec interface
	Codec interface {
		// Encode encodes the provided value into a byte slice.
		Encode(interface{}) ([]byte, error)

		// Decode decodes the provided byte slice into a value.
		Decode([]byte, interface{}) error
	}

	// default codec
	codec struct {
	}
)

var _ Codec = (*codec)(nil)

// Encode encodes the given value into a JSON byte array.
func (c *codec) Encode(v interface{}) ([]byte, error) {
	return json.Marshal(v)
}

// Decode decodes binary data into a value pointed to by v using JSON encoding.
func (c *codec) Decode(data []byte, v interface{}) error {
	return json.Unmarshal(data, v)
}
