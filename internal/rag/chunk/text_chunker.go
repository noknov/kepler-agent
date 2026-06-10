package chunk

import (
	"strings"
)

// TextChunker splits files by blank-line-separated paragraphs or heading
// boundaries (for Markdown). Used as a fallback for languages without a
// dedicated chunker.
type TextChunker struct {
	MaxLines int // max lines per chunk; 0 = 120
}

func (t TextChunker) Languages() []string { return nil }

func (t TextChunker) Chunk(path, content string) ([]Chunk, error) {
	maxLines := t.MaxLines
	if maxLines <= 0 {
		maxLines = 120
	}

	lang := DetectLanguage(path)
	if lang == "markdown" {
		return t.chunkMarkdown(path, content, maxLines), nil
	}

	lines := strings.Split(content, "\n")
	if len(lines) <= maxLines {
		c := Chunk{
			StartLine: 1,
			EndLine:   len(lines),
			ChunkType: TypeBlock,
			Content:   content,
		}
		fillChunkFields([]Chunk{c}, "", path, lang)
		return []Chunk{c}, nil
	}

	var chunks []Chunk
	start := 0
	for start < len(lines) {
		end := start + maxLines
		if end > len(lines) {
			end = len(lines)
		}
		if end < len(lines) {
			best := end
			for i := end - 1; i >= start+maxLines/2; i-- {
				if strings.TrimSpace(lines[i]) == "" {
					best = i + 1
					break
				}
			}
			end = best
		}
		c := Chunk{
			StartLine: start + 1,
			EndLine:   end,
			ChunkType: TypeBlock,
			Content:   joinLines(lines, start, end),
		}
		chunks = append(chunks, c)
		start = end
	}

	fillChunkFields(chunks, "", path, lang)
	return chunks, nil
}

func (t TextChunker) chunkMarkdown(path, content string, maxLines int) []Chunk {
	lines := strings.Split(content, "\n")
	var chunks []Chunk
	sectionStart := 0
	sectionName := ""

	flush := func(end int) {
		if end <= sectionStart {
			return
		}
		body := joinLines(lines, sectionStart, end)
		if strings.TrimSpace(body) == "" {
			return
		}
		c := Chunk{
			StartLine:  sectionStart + 1,
			EndLine:    end,
			ChunkType:  TypeBlock,
			SymbolName: sectionName,
			Content:    body,
		}
		chunks = append(chunks, c)
	}

	for i, line := range lines {
		if strings.HasPrefix(line, "#") {
			if i > sectionStart {
				flush(i)
			}
			sectionStart = i
			sectionName = strings.TrimSpace(strings.TrimLeft(line, "#"))
		}
		if i-sectionStart >= maxLines && i > sectionStart {
			flush(i)
			sectionStart = i
		}
	}
	flush(len(lines))

	fillChunkFields(chunks, "", path, "markdown")
	return chunks
}
