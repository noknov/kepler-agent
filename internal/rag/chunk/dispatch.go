package chunk

// Dispatcher routes files to the appropriate Chunker based on language.
type Dispatcher struct {
	goChunker   GoChunker
	csChunker   *ScopeChunker
	jsChunker   *ScopeChunker
	tsChunker   *ScopeChunker
	textChunker TextChunker
}

func NewDispatcher() *Dispatcher {
	return &Dispatcher{
		goChunker:   GoChunker{},
		csChunker:   NewCSharpChunker(),
		jsChunker:   NewJavaScriptChunker("javascript"),
		tsChunker:   NewJavaScriptChunker("typescript"),
		textChunker: TextChunker{},
	}
}

func (d *Dispatcher) Chunk(path, content string) ([]Chunk, error) {
	lang := DetectLanguage(path)
	switch lang {
	case "go":
		return d.goChunker.Chunk(path, content)
	case "csharp":
		return d.csChunker.Chunk(path, content)
	case "javascript", "jsx":
		return d.jsChunker.Chunk(path, content)
	case "typescript", "tsx":
		return d.tsChunker.Chunk(path, content)
	default:
		return d.textChunker.Chunk(path, content)
	}
}
