package llm

import "encoding/json"

// responsesStreamOutput retains completed item receipts until the response
// terminates. ChatGPT streams can send output:[] in the terminal envelope even
// though output_item.done already carried the complete text or function call.
// Raw items retain opaque continuation fields that the typed view does not own.
type responsesStreamOutput struct {
	items map[int]json.RawMessage
	bytes int
	err   error
}

func (s *responsesStreamOutput) add(index *int, item json.RawMessage) {
	const maxCompletedBytes = 8 << 20
	if s.err != nil {
		return
	}
	if index == nil || *index < 0 || *index >= maxStreamToolCalls {
		s.err = ProtocolError{Provider: "responses", Detail: "invalid completed output index"}
		return
	}
	if _, exists := s.items[*index]; exists {
		s.err = ProtocolError{Provider: "responses", Detail: "duplicate completed output index"}
		return
	}
	if s.bytes+len(item) > maxCompletedBytes {
		s.err = ProtocolError{Provider: "responses", Detail: "completed output exceeds the continuation byte budget"}
		return
	}
	if s.items == nil {
		s.items = make(map[int]json.RawMessage)
	}
	s.items[*index] = item
	s.bytes += len(item)
}

func (s *responsesStreamOutput) populate(response *responsesResponse) error {
	// A populated terminal snapshot is authoritative. Failed and cancelled
	// responses retain their original recovery semantics and never expose calls.
	if len(response.Output) != 0 || (response.Status != "completed" && response.Status != "incomplete") {
		return nil
	}
	if s.err != nil {
		return s.err
	}
	items := make([]responsesOutput, 0, len(s.items))
	raw := make([]json.RawMessage, 0, len(s.items))
	for index := 0; index < len(s.items); index++ {
		data, ok := s.items[index]
		if !ok {
			return ProtocolError{Provider: "responses", Detail: "completed output indexes are not contiguous"}
		}
		var item responsesOutput
		if json.Unmarshal(data, &item) != nil || item.Type == "" || item.ID == "" {
			return ProtocolError{Provider: "responses", Detail: "invalid completed output item"}
		}
		items = append(items, item)
		raw = append(raw, data)
	}
	response.Output, response.RawOutput = items, raw
	return nil
}
