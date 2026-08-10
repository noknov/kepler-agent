package sse

import (
	"bufio"
	"bytes"
	"io"
	"strings"
)

type Event struct {
	Name string
	Data []byte
}

func Read(reader io.Reader, handle func(Event) error) error {
	scanner := bufio.NewScanner(reader)
	scanner.Buffer(make([]byte, 64<<10), 16<<20)
	var name string
	var data bytes.Buffer
	flush := func() error {
		if data.Len() == 0 && name == "" {
			return nil
		}
		payload := bytes.TrimSuffix(data.Bytes(), []byte("\n"))
		err := handle(Event{Name: name, Data: append([]byte(nil), payload...)})
		name = ""
		data.Reset()
		return err
	}
	for scanner.Scan() {
		line := scanner.Text()
		if line == "" {
			if err := flush(); err != nil {
				return err
			}
			continue
		}
		if strings.HasPrefix(line, "event:") {
			name = strings.TrimSpace(strings.TrimPrefix(line, "event:"))
			continue
		}
		if strings.HasPrefix(line, "data:") {
			data.WriteString(strings.TrimSpace(strings.TrimPrefix(line, "data:")))
			data.WriteByte('\n')
		}
	}
	if err := scanner.Err(); err != nil {
		return err
	}
	return flush()
}
