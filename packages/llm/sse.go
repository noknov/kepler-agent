package llm

import (
	"bufio"
	"bytes"
	"errors"
	"io"
	"strings"
)

const maxSSEEventBytes = 8 << 20

type sseEvent struct {
	Event string
	Data  string
}

func readSSE(r io.Reader, fn func(ev sseEvent) bool) error {
	scanner := bufio.NewScanner(r)
	scanner.Buffer(make([]byte, 0, 64*1024), maxSSEEventBytes)
	scanner.Split(scanSSELines())
	var event string
	var data strings.Builder
	var hasData bool
	firstLine := true
	for scanner.Scan() {
		line := scanner.Bytes()
		if firstLine {
			line = bytes.TrimPrefix(line, []byte("\xef\xbb\xbf"))
			firstLine = false
		}
		if len(line) == 0 {
			if hasData && !fn(sseEvent{Event: event, Data: data.String()}) {
				return nil
			}
			event = ""
			data.Reset()
			hasData = false
			continue
		}
		field, value, _ := bytes.Cut(line, []byte(":"))
		value = bytes.TrimPrefix(value, []byte(" "))
		switch string(field) {
		case "event":
			if len(value)+data.Len() > maxSSEEventBytes {
				return ProtocolError{Provider: "sse", Detail: "event exceeds byte budget"}
			}
			event = string(value)
		case "data":
			size := len(event) + data.Len() + len(value)
			if hasData {
				size++
			}
			if size > maxSSEEventBytes {
				return ProtocolError{Provider: "sse", Detail: "event exceeds byte budget"}
			}
			if hasData {
				data.WriteByte('\n')
			}
			data.Write(value)
			hasData = true
		}
	}
	if err := scanner.Err(); err != nil {
		if errors.Is(err, bufio.ErrTooLong) {
			return ProtocolError{Provider: "sse", Detail: "event line exceeds byte budget"}
		}
		return err
	}
	// A final blank line is conventional but not required for a clean EOF.
	if hasData {
		fn(sseEvent{Event: event, Data: data.String()})
	}
	return nil
}

// Dispatch CR immediately, then consume an optional LF on the next scan. A
// terminal event must not wait for another network byte to disambiguate CRLF.
func scanSSELines() bufio.SplitFunc {
	afterCR := false
	return func(data []byte, atEOF bool) (int, []byte, error) {
		if afterCR && len(data) > 0 {
			afterCR = false
			if data[0] == '\n' {
				return 1, nil, nil
			}
		}
		if index := bytes.IndexAny(data, "\r\n"); index >= 0 {
			afterCR = data[index] == '\r'
			return index + 1, data[:index], nil
		}
		if atEOF && len(data) > 0 {
			return len(data), data, nil
		}
		return 0, nil, nil
	}
}
