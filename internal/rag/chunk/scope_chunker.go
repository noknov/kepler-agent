package chunk

import (
	"regexp"
	"strings"
)

// ScopeChunker splits source files by tracking brace depth and matching
// language-specific declaration patterns. Works for C#, JS, TS, Java, etc.
type ScopeChunker struct {
	Lang     string
	patterns []*declPattern
	maxLines int
}

type declPattern struct {
	re        *regexp.Regexp
	chunkType Type
	nameGroup int // capture group index for the symbol name
}

func NewCSharpChunker() *ScopeChunker {
	return &ScopeChunker{
		Lang:     "csharp",
		maxLines: 200,
		patterns: []*declPattern{
			{re: regexp.MustCompile(`^\s*(?:public|private|protected|internal|static|abstract|sealed|partial|async|override|virtual|\s)*\s*namespace\s+(\S+)`), chunkType: TypeBlock, nameGroup: 1},
			{re: regexp.MustCompile(`^\s*(?:public|private|protected|internal|static|abstract|sealed|partial|\s)*\s*(?:class|struct|interface|enum|record)\s+(\w+)`), chunkType: TypeType, nameGroup: 1},
			{re: regexp.MustCompile(`^\s*(?:public|private|protected|internal|static|abstract|sealed|partial|virtual|override|async|\s)*\s*(?:void|int|string|bool|long|double|float|decimal|char|byte|object|var|Task|IActionResult|ActionResult|IEnumerable|List|Dictionary|\w+(?:<[^>]+>)?)\s+(\w+)\s*(?:<[^>]*>)?\s*\(`), chunkType: TypeFunction, nameGroup: 1},
			{re: regexp.MustCompile(`^\s*(?:public|private|protected|internal|static|abstract|sealed|partial|virtual|override|async|\s)*\s*(\w+(?:<[^>]+>)?)\s+(\w+)\s*\{`), chunkType: TypeVar, nameGroup: 2},
		},
	}
}

func NewJavaScriptChunker(lang string) *ScopeChunker {
	return &ScopeChunker{
		Lang:     lang,
		maxLines: 200,
		patterns: []*declPattern{
			{re: regexp.MustCompile(`^\s*(?:export\s+)?(?:default\s+)?class\s+(\w+)`), chunkType: TypeType, nameGroup: 1},
			{re: regexp.MustCompile(`^\s*(?:export\s+)?(?:default\s+)?(?:async\s+)?function\s*\*?\s*(\w+)`), chunkType: TypeFunction, nameGroup: 1},
			{re: regexp.MustCompile(`^\s*(?:export\s+)?(?:const|let|var)\s+(\w+)\s*=\s*(?:async\s+)?(?:function|\(|[^=]*=>)`), chunkType: TypeFunction, nameGroup: 1},
			{re: regexp.MustCompile(`^\s*(?:export\s+)?(?:const|let|var)\s+(\w+)\s*=`), chunkType: TypeVar, nameGroup: 1},
			{re: regexp.MustCompile(`^\s*(?:public|private|protected|static|async|get|set|\s)*\s*(\w+)\s*\([^)]*\)\s*(?::\s*\S+\s*)?\{`), chunkType: TypeMethod, nameGroup: 1},
			{re: regexp.MustCompile(`^\s*(?:export\s+)?(?:type|interface|enum)\s+(\w+)`), chunkType: TypeType, nameGroup: 1},
		},
	}
}

func (sc *ScopeChunker) Languages() []string { return []string{sc.Lang} }

func (sc *ScopeChunker) Chunk(path, content string) ([]Chunk, error) {
	lines := strings.Split(content, "\n")
	if len(lines) == 0 {
		return nil, nil
	}

	header := extractHeader(lines, sc.Lang)

	type region struct {
		startLine  int
		endLine    int
		name       string
		parent     string
		chunkType  Type
		braceDepth int
	}

	var regions []region
	depth := 0
	var stack []region

	for i, line := range lines {
		lineNo := i + 1
		trimmed := strings.TrimSpace(line)

		if trimmed == "" || strings.HasPrefix(trimmed, "//") || strings.HasPrefix(trimmed, "#") {
			depth += countBraces(line)
			continue
		}

		matched := false
		for _, pat := range sc.patterns {
			m := pat.re.FindStringSubmatch(line)
			if m == nil {
				continue
			}
			name := ""
			if pat.nameGroup > 0 && pat.nameGroup < len(m) {
				name = m[pat.nameGroup]
			}

			parentName := ""
			ct := pat.chunkType
			if len(stack) > 0 {
				top := stack[len(stack)-1]
				if top.chunkType == TypeType && (ct == TypeFunction || ct == TypeMethod) {
					parentName = top.name
					ct = TypeMethod
					name = top.name + "." + name
				}
			}

			r := region{
				startLine:  lineNo,
				name:       name,
				parent:     parentName,
				chunkType:  ct,
				braceDepth: depth,
			}
			stack = append(stack, r)
			matched = true
			break
		}

		newDepth := depth + countBraces(line)

		if !matched && len(stack) > 0 {
			top := &stack[len(stack)-1]
			if newDepth <= top.braceDepth && lineNo > top.startLine {
				top.endLine = lineNo
				regions = append(regions, *top)
				stack = stack[:len(stack)-1]
			}
		}
		if matched && len(stack) > 1 {
			prev := &stack[len(stack)-2]
			if newDepth <= prev.braceDepth && i > 0 {
				prev.endLine = lineNo - 1
				regions = append(regions, *prev)
				last := stack[len(stack)-1]
				stack = stack[:len(stack)-2]
				stack = append(stack, last)
			}
		}

		depth = newDepth
	}

	for len(stack) > 0 {
		top := &stack[len(stack)-1]
		top.endLine = len(lines)
		regions = append(regions, *top)
		stack = stack[:len(stack)-1]
	}

	if len(regions) == 0 {
		return []Chunk{{
			StartLine: 1,
			EndLine:   len(lines),
			ChunkType: TypeBlock,
			Content:   content,
		}}, nil
	}

	var chunks []Chunk
	for _, r := range regions {
		body := joinLines(lines, r.startLine-1, r.endLine)
		chunks = append(chunks, Chunk{
			StartLine:     r.startLine,
			EndLine:       r.endLine,
			ChunkType:     r.chunkType,
			SymbolName:    r.name,
			ParentSymbol:  r.parent,
			Content:       body,
			ContextPrefix: header,
		})
	}

	fillChunkFields(chunks, "", path, sc.Lang)
	return chunks, nil
}

func countBraces(line string) int {
	n := 0
	inStr := byte(0)
	escaped := false
	for i := 0; i < len(line); i++ {
		c := line[i]
		if escaped {
			escaped = false
			continue
		}
		if c == '\\' && inStr != 0 {
			escaped = true
			continue
		}
		if inStr != 0 {
			if c == inStr {
				inStr = 0
			}
			continue
		}
		switch c {
		case '"', '\'', '`':
			inStr = c
		case '/':
			if i+1 < len(line) && line[i+1] == '/' {
				return n
			}
		case '{':
			n++
		case '}':
			n--
		}
	}
	return n
}

func extractHeader(lines []string, lang string) string {
	var b strings.Builder
	limit := 30
	if limit > len(lines) {
		limit = len(lines)
	}
	for i := 0; i < limit; i++ {
		trimmed := strings.TrimSpace(lines[i])
		switch lang {
		case "csharp":
			if strings.HasPrefix(trimmed, "using ") || strings.HasPrefix(trimmed, "namespace ") || trimmed == "" {
				b.WriteString(lines[i])
				b.WriteByte('\n')
				continue
			}
			return strings.TrimSpace(b.String())
		case "javascript", "typescript", "tsx", "jsx":
			if strings.HasPrefix(trimmed, "import ") || strings.HasPrefix(trimmed, "require(") ||
				strings.HasPrefix(trimmed, "const ") && strings.Contains(trimmed, "require(") ||
				strings.HasPrefix(trimmed, "//") || strings.HasPrefix(trimmed, "/*") ||
				strings.HasPrefix(trimmed, "* ") || strings.HasPrefix(trimmed, "*/") ||
				trimmed == "" || strings.HasPrefix(trimmed, "'use strict'") || strings.HasPrefix(trimmed, "\"use strict\"") {
				b.WriteString(lines[i])
				b.WriteByte('\n')
				continue
			}
			return strings.TrimSpace(b.String())
		default:
			if trimmed == "" || strings.HasPrefix(trimmed, "//") || strings.HasPrefix(trimmed, "#") || strings.HasPrefix(trimmed, "import") || strings.HasPrefix(trimmed, "using") {
				b.WriteString(lines[i])
				b.WriteByte('\n')
				continue
			}
			return strings.TrimSpace(b.String())
		}
	}
	return strings.TrimSpace(b.String())
}
