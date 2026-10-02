// Package postgresjson encodes JSON accepted by PostgreSQL JSONB.
package postgresjson

import "encoding/json"

// Marshal replaces actual NUL characters with U+FFFD. It walks JSON escapes
// instead of decoding through interface{} (which loses large integer precision)
// or replacing raw substrings (which corrupts literal backslash-u text).
func Marshal(value any) ([]byte, error) {
	data, err := json.Marshal(value)
	if err != nil {
		return nil, err
	}
	for i := 0; i+1 < len(data); i++ {
		if data[i] != '\\' {
			continue
		}
		if i+6 <= len(data) && string(data[i:i+6]) == `\u0000` {
			copy(data[i+2:i+6], "fffd")
			i += 5
		} else {
			i++ // Consume the escaped byte, especially the second backslash in \\.
		}
	}
	return data, nil
}
