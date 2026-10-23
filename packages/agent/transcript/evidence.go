package transcript

import (
	"fmt"
	"unicode/utf8"

	"github.com/noknov/kepler-agent/packages/agent/model"
)

type EvidencePage struct {
	Sequence   uint64         `json:"sequence"`
	Block      int            `json:"block"`
	Blocks     int            `json:"blocks"`
	Offset     int            `json:"offset"`
	NextOffset int            `json:"next_offset"`
	Bytes      int            `json:"bytes"`
	EOF        bool           `json:"eof"`
	Text       string         `json:"text,omitempty"`
	Image      *model.Content `json:"-"`
}

func ToolEvidence(event Event, block, offset, limit int) (EvidencePage, error) {
	page := EvidencePage{Sequence: event.Sequence, Block: block}
	if (event.Type != ToolCallCompleted && event.Type != ToolCallFailed) || event.ToolResult == nil || block < 0 || block >= len(event.ToolResult.Content) || offset < 0 {
		return page, fmt.Errorf("requested block is not tool evidence")
	}
	page.Blocks = len(event.ToolResult.Content)
	content := event.ToolResult.Content[block]
	var text string
	switch content.Type {
	case model.ContentImage:
		page.Image = &content
		page.EOF = true
		return page, nil
	case model.ContentText:
		text = content.Text
	case model.ContentJSON:
		text = string(content.JSON)
	default:
		return page, fmt.Errorf("requested block is not textual evidence")
	}
	if offset > len(text) {
		return page, fmt.Errorf("offset exceeds evidence length")
	}
	if limit <= 0 || limit > 12000 {
		limit = 12000
	}
	start := offset
	for start < len(text) && !utf8.RuneStart(text[start]) {
		start++
	}
	end := min(len(text), start+limit)
	for end > start && end < len(text) && !utf8.RuneStart(text[end]) {
		end--
	}
	if end == start && start < len(text) {
		return page, fmt.Errorf("limit is too small for one UTF-8 character")
	}
	page.Offset, page.NextOffset, page.Bytes, page.EOF, page.Text = start, end, len(text), end == len(text), text[start:end]
	return page, nil
}
