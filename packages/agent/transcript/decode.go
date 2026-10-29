package transcript

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
)

// DecodeEvent preserves exact numbers in untyped metadata as json.Number.
// A canonical record contains one complete JSON value, never a partial stream.
func DecodeEvent(data []byte) (Event, error) {
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.UseNumber()
	var event Event
	if err := decoder.Decode(&event); err != nil {
		return Event{}, err
	}
	if decoder.Decode(new(any)) != io.EOF {
		return Event{}, fmt.Errorf("transcript event has trailing data")
	}
	return event, nil
}
