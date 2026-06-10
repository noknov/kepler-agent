package chunk

import (
	"strings"
	"testing"
)

func TestDetectLanguage(t *testing.T) {
	tests := []struct {
		path string
		want string
	}{
		{"main.go", "go"},
		{"service.cs", "csharp"},
		{"app.js", "javascript"},
		{"component.tsx", "tsx"},
		{"index.ts", "typescript"},
		{"readme.md", "markdown"},
		{"config.yaml", "yaml"},
		{"Makefile", ""},
		{"unknown.xyz", ""},
		{"path/to/Handler.cs", "csharp"},
	}
	for _, tt := range tests {
		t.Run(tt.path, func(t *testing.T) {
			got := DetectLanguage(tt.path)
			if got != tt.want {
				t.Errorf("DetectLanguage(%q) = %q, want %q", tt.path, got, tt.want)
			}
		})
	}
}

func TestShouldIndex(t *testing.T) {
	tests := []struct {
		path string
		want bool
	}{
		{"main.go", true},
		{"service.cs", true},
		{"app.js", true},
		{"readme.md", true},
		{"Dockerfile", true},
		{"Makefile", true},
		{".gitignore", false},
		{"image.png", false},
		{"data.bin", false},
	}
	for _, tt := range tests {
		t.Run(tt.path, func(t *testing.T) {
			got := ShouldIndex(tt.path)
			if got != tt.want {
				t.Errorf("ShouldIndex(%q) = %v, want %v", tt.path, got, tt.want)
			}
		})
	}
}

func TestGoChunker(t *testing.T) {
	src := `package example

import "fmt"

const Version = "1.0"

type Server struct {
	Addr string
	Port int
}

func NewServer(addr string) *Server {
	return &Server{Addr: addr, Port: 8080}
}

func (s *Server) Start() error {
	fmt.Println("starting", s.Addr)
	return nil
}

func (s *Server) Stop() {
	fmt.Println("stopped")
}
`
	chunker := GoChunker{}
	chunks, err := chunker.Chunk("server.go", src)
	if err != nil {
		t.Fatal(err)
	}

	if len(chunks) < 4 {
		t.Fatalf("expected at least 4 chunks (header + const + type + 2 funcs), got %d", len(chunks))
	}

	types := map[Type]int{}
	for _, c := range chunks {
		types[c.ChunkType]++
		if c.ID == "" {
			t.Error("chunk has empty ID")
		}
		if c.ContentHash == "" {
			t.Error("chunk has empty ContentHash")
		}
	}

	if types[TypeFileHeader] != 1 {
		t.Errorf("expected 1 file_header chunk, got %d", types[TypeFileHeader])
	}
	if types[TypeConst] != 1 {
		t.Errorf("expected 1 const chunk, got %d", types[TypeConst])
	}
	if types[TypeType] != 1 {
		t.Errorf("expected 1 type chunk, got %d", types[TypeType])
	}

	var methodCount int
	for _, c := range chunks {
		if c.ChunkType == TypeMethod {
			methodCount++
			if c.ParentSymbol != "Server" {
				t.Errorf("method %q has parent %q, want Server", c.SymbolName, c.ParentSymbol)
			}
			if !strings.Contains(c.ContextPrefix, "type Server struct") {
				t.Errorf("method %q context should contain Server type def", c.SymbolName)
			}
		}
	}
	if methodCount != 2 {
		t.Errorf("expected 2 method chunks (Start, Stop), got %d", methodCount)
	}
}

func TestGoChunker_LongFunction(t *testing.T) {
	var b strings.Builder
	b.WriteString("package big\n\nfunc VeryLong() {\n")
	for i := 0; i < 300; i++ {
		b.WriteString("\tx := " + strings.Repeat("a", 10) + "\n")
		if i%50 == 0 && i > 0 {
			b.WriteString("\n") // blank line for split point
		}
	}
	b.WriteString("}\n")

	chunker := GoChunker{MaxLines: 100}
	chunks, err := chunker.Chunk("big.go", b.String())
	if err != nil {
		t.Fatal(err)
	}

	funcChunks := 0
	for _, c := range chunks {
		if c.ChunkType == TypeFunction {
			funcChunks++
		}
	}
	if funcChunks < 2 {
		t.Errorf("expected long function to be split into >=2 chunks, got %d", funcChunks)
	}
}

func TestScopeChunker_CSharp(t *testing.T) {
	src := `using System;
using System.Collections.Generic;

namespace MyApp.Services
{
    public class UserService
    {
        private readonly IUserRepo _repo;

        public UserService(IUserRepo repo)
        {
            _repo = repo;
        }

        public async Task<User> GetUser(int id)
        {
            return await _repo.FindById(id);
        }

        public void DeleteUser(int id)
        {
            _repo.Delete(id);
        }
    }
}
`
	chunker := NewCSharpChunker()
	chunks, err := chunker.Chunk("UserService.cs", src)
	if err != nil {
		t.Fatal(err)
	}

	if len(chunks) == 0 {
		t.Fatal("expected chunks for C# file, got 0")
	}

	hasClass := false
	for _, c := range chunks {
		if c.ChunkType == TypeType && strings.Contains(c.SymbolName, "UserService") {
			hasClass = true
		}
		if c.Language != "csharp" {
			t.Errorf("chunk language = %q, want csharp", c.Language)
		}
	}
	if !hasClass {
		t.Error("expected a type chunk for UserService class")
	}
}

func TestScopeChunker_JavaScript(t *testing.T) {
	src := `import express from 'express';
import { UserService } from './services';

const PORT = 3000;

export class App {
    constructor() {
        this.server = express();
    }

    start() {
        this.server.listen(PORT);
    }
}

export function createApp() {
    return new App();
}

const handler = async (req, res) => {
    res.json({ ok: true });
};
`
	chunker := NewJavaScriptChunker("javascript")
	chunks, err := chunker.Chunk("app.js", src)
	if err != nil {
		t.Fatal(err)
	}

	if len(chunks) == 0 {
		t.Fatal("expected chunks for JS file, got 0")
	}

	for _, c := range chunks {
		if c.Language != "javascript" {
			t.Errorf("chunk language = %q, want javascript", c.Language)
		}
		if c.ContextPrefix == "" {
			t.Errorf("chunk %q has no context prefix", c.SymbolName)
		}
	}
}

func TestTextChunker_Markdown(t *testing.T) {
	src := `# Title

Introduction paragraph.

## Section One

Content for section one.
More content here.

## Section Two

Content for section two.
`
	chunker := TextChunker{}
	chunks, err := chunker.Chunk("readme.md", src)
	if err != nil {
		t.Fatal(err)
	}

	if len(chunks) < 2 {
		t.Fatalf("expected at least 2 markdown chunks, got %d", len(chunks))
	}

	for _, c := range chunks {
		if c.ChunkType != TypeBlock {
			t.Errorf("markdown chunk type = %q, want block", c.ChunkType)
		}
	}
}

func TestDispatcher(t *testing.T) {
	d := NewDispatcher()

	goSrc := "package main\n\nfunc main() {}\n"
	chunks, err := d.Chunk("main.go", goSrc)
	if err != nil {
		t.Fatal(err)
	}
	if len(chunks) == 0 {
		t.Error("dispatcher returned 0 chunks for Go file")
	}

	jsSrc := "export function hello() { return 'hi'; }\n"
	chunks, err = d.Chunk("hello.js", jsSrc)
	if err != nil {
		t.Fatal(err)
	}
	if len(chunks) == 0 {
		t.Error("dispatcher returned 0 chunks for JS file")
	}

	txtSrc := "some config\nkey=value\n"
	chunks, err = d.Chunk("config.txt", txtSrc)
	if err != nil {
		t.Fatal(err)
	}
	if len(chunks) == 0 {
		t.Error("dispatcher returned 0 chunks for text file")
	}
}

func TestJoinLines(t *testing.T) {
	lines := []string{"a", "b", "c", "d"}
	got := joinLines(lines, 1, 3)
	want := "b\nc"
	if got != want {
		t.Errorf("joinLines = %q, want %q", got, want)
	}
}

func TestCountBraces(t *testing.T) {
	tests := []struct {
		line string
		want int
	}{
		{"func main() {", 1},
		{"}", -1},
		{`s := "{ not a brace }"`, 0},
		{"if x { y() } else {", 1},
		{"// { comment brace", 0},
	}
	for _, tt := range tests {
		got := countBraces(tt.line)
		if got != tt.want {
			t.Errorf("countBraces(%q) = %d, want %d", tt.line, got, tt.want)
		}
	}
}
