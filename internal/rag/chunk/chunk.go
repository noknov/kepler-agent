package chunk

import (
	"crypto/sha256"
	"fmt"
	"path/filepath"
	"strings"
)

type Type string

const (
	TypeFunction   Type = "function"
	TypeMethod     Type = "method"
	TypeType       Type = "type"
	TypeConst      Type = "const"
	TypeVar        Type = "var"
	TypeFileHeader Type = "file_header"
	TypeBlock      Type = "block"
)

type Chunk struct {
	ID            string
	RepoPath      string
	FilePath      string
	Branch        string
	CommitSHA     string
	StartLine     int
	EndLine       int
	ChunkType     Type
	Language      string
	SymbolName    string
	ParentSymbol  string
	Package       string
	Content       string
	ContextPrefix string
	ContentHash   string
}

func (c *Chunk) ComputeID() {
	h := sha256.Sum256([]byte(c.RepoPath + "\x00" + c.Branch + "\x00" + c.FilePath + "\x00" + c.Content))
	c.ID = fmt.Sprintf("%x", h[:12])
}

func (c *Chunk) ComputeContentHash() {
	h := sha256.Sum256([]byte(c.Content))
	c.ContentHash = fmt.Sprintf("%x", h[:16])
}

type Chunker interface {
	Chunk(path, content string) ([]Chunk, error)
	Languages() []string
}

func DetectLanguage(path string) string {
	ext := strings.ToLower(filepath.Ext(path))
	switch ext {
	case ".go":
		return "go"
	case ".cs":
		return "csharp"
	case ".js", ".mjs", ".cjs":
		return "javascript"
	case ".ts", ".mts", ".cts":
		return "typescript"
	case ".tsx":
		return "tsx"
	case ".jsx":
		return "jsx"
	case ".py":
		return "python"
	case ".java":
		return "java"
	case ".md", ".markdown":
		return "markdown"
	case ".yaml", ".yml":
		return "yaml"
	case ".json":
		return "json"
	case ".sql":
		return "sql"
	case ".sh", ".bash":
		return "shell"
	case ".css", ".scss", ".less":
		return "css"
	case ".html", ".htm":
		return "html"
	case ".xml", ".csproj", ".sln":
		return "xml"
	case ".proto":
		return "protobuf"
	case ".rb":
		return "ruby"
	case ".rs":
		return "rust"
	default:
		return ""
	}
}

func ShouldIndex(path string) bool {
	lang := DetectLanguage(path)
	if lang != "" {
		return true
	}
	base := strings.ToLower(filepath.Base(path))
	switch base {
	case "dockerfile", "makefile", "rakefile", "gemfile":
		return true
	}
	return false
}

func fillChunkFields(chunks []Chunk, repoPath, filePath, lang string) {
	for i := range chunks {
		chunks[i].RepoPath = repoPath
		chunks[i].FilePath = filePath
		chunks[i].Language = lang
		chunks[i].ComputeContentHash()
		chunks[i].ComputeID()
	}
}
