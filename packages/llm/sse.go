package llm

import (
	"bufio"
	"io"
	"strings"
)

type sseEvent struct {
	Event string
	Data  string
}

func readSSE(r io.Reader, fn func(ev sseEvent) bool) error {
	scanner := bufio.NewScanner(r)
	scanner.Buffer(make([]byte, 0, 64*1024), 1<<20)
	var event, data strings.Builder
	for scanner.Scan() {
		line := scanner.Text()
		if line == "" {
			if data.Len() > 0 {
				if !fn(sseEvent{Event: event.String(), Data: data.String()}) {
					return nil
				}
			}
			event.Reset()
			data.Reset()
			continue
		}
		if strings.HasPrefix(line, "event:") {
			event.WriteString(strings.TrimSpace(strings.TrimPrefix(line, "event:")))
		} else if strings.HasPrefix(line, "data:") {
			if data.Len() > 0 {
				data.WriteByte('\n')
			}
			data.WriteString(strings.TrimSpace(strings.TrimPrefix(line, "data:")))
		}
	}
	if err := scanner.Err(); err != nil {
		return err
	}
	// A final blank line is conventional but not required for a clean EOF.
	if data.Len() > 0 {
		fn(sseEvent{Event: event.String(), Data: data.String()})
	}
	return nil
}
