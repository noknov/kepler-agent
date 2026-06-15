package chunk

import (
	"go/ast"
	"go/parser"
	"go/token"
	"strings"
)

type GoChunker struct {
	MaxLines int // split functions longer than this; 0 = 200
}

func (g GoChunker) Languages() []string { return []string{"go"} }

func (g GoChunker) Chunk(path, content string) ([]Chunk, error) {
	maxLines := g.MaxLines
	if maxLines <= 0 {
		maxLines = 200
	}

	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, path, content, parser.ParseComments)
	if err != nil {
		chunks := fallbackChunks(path, content)
		fillChunkFields(chunks, "", path, "go")
		return chunks, nil
	}

	lines := strings.Split(content, "\n")
	header := buildGoHeader(f, fset, lines)

	var chunks []Chunk

	headerEnd := 0
	if len(f.Decls) > 0 {
		headerEnd = fset.Position(f.Decls[0].Pos()).Line - 1
	} else {
		headerEnd = len(lines)
	}
	if headerEnd > 0 {
		chunks = append(chunks, Chunk{
			StartLine: 1,
			EndLine:   headerEnd,
			ChunkType: TypeFileHeader,
			Package:   f.Name.Name,
			Content:   joinLines(lines, 0, headerEnd),
		})
	}

	typeNames := collectTypeNames(f, fset)

	for _, decl := range f.Decls {
		switch d := decl.(type) {
		case *ast.FuncDecl:
			start := fset.Position(d.Pos()).Line
			end := fset.Position(d.End()).Line
			name := d.Name.Name
			parent := ""
			chunkType := TypeFunction
			if d.Recv != nil && len(d.Recv.List) > 0 {
				chunkType = TypeMethod
				parent = receiverTypeName(d.Recv.List[0].Type)
				name = parent + "." + name
			}

			body := joinLines(lines, start-1, end)
			ctx := header
			if parent != "" {
				if typeDef, ok := typeNames[parent]; ok {
					ctx += "\n" + typeDef
				}
			}

			if end-start+1 > maxLines {
				subs := splitLongFunction(lines, start, end, maxLines)
				for _, sub := range subs {
					chunks = append(chunks, Chunk{
						StartLine:     sub.start,
						EndLine:       sub.end,
						ChunkType:     chunkType,
						SymbolName:    name,
						ParentSymbol:  parent,
						Package:       f.Name.Name,
						Content:       sub.content,
						ContextPrefix: ctx,
					})
				}
			} else {
				chunks = append(chunks, Chunk{
					StartLine:     start,
					EndLine:       end,
					ChunkType:     chunkType,
					SymbolName:    name,
					ParentSymbol:  parent,
					Package:       f.Name.Name,
					Content:       body,
					ContextPrefix: ctx,
				})
			}

		case *ast.GenDecl:
			start := fset.Position(d.Pos()).Line
			end := fset.Position(d.End()).Line
			var chunkType Type
			switch d.Tok {
			case token.TYPE:
				chunkType = TypeType
			case token.CONST:
				chunkType = TypeConst
			case token.VAR:
				chunkType = TypeVar
			default:
				continue
			}
			name := genDeclName(d)
			if name == "" {
				name = d.Tok.String()
			}
			chunks = append(chunks, Chunk{
				StartLine:     start,
				EndLine:       end,
				ChunkType:     chunkType,
				SymbolName:    name,
				Package:       f.Name.Name,
				Content:       joinLines(lines, start-1, end),
				ContextPrefix: header,
			})
		}
	}

	fillChunkFields(chunks, "", path, "go")
	return chunks, nil
}

func buildGoHeader(f *ast.File, fset *token.FileSet, lines []string) string {
	var b strings.Builder
	b.WriteString("package " + f.Name.Name + "\n")
	for _, imp := range f.Imports {
		start := fset.Position(imp.Pos()).Line
		end := fset.Position(imp.End()).Line
		for i := start - 1; i < end && i < len(lines); i++ {
			b.WriteString(lines[i])
			b.WriteByte('\n')
		}
	}
	return strings.TrimSpace(b.String())
}

func collectTypeNames(f *ast.File, fset *token.FileSet) map[string]string {
	result := make(map[string]string)
	for _, decl := range f.Decls {
		gd, ok := decl.(*ast.GenDecl)
		if !ok || gd.Tok != token.TYPE {
			continue
		}
		for _, spec := range gd.Specs {
			ts, ok := spec.(*ast.TypeSpec)
			if !ok {
				continue
			}
			var b strings.Builder
			b.WriteString("type " + ts.Name.Name + " ")
			switch ts.Type.(type) {
			case *ast.StructType:
				b.WriteString("struct { ... }")
			case *ast.InterfaceType:
				b.WriteString("interface { ... }")
			default:
				b.WriteString("...")
			}
			result[ts.Name.Name] = b.String()
		}
	}
	return result
}

func receiverTypeName(expr ast.Expr) string {
	switch t := expr.(type) {
	case *ast.StarExpr:
		return receiverTypeName(t.X)
	case *ast.Ident:
		return t.Name
	case *ast.IndexExpr:
		return receiverTypeName(t.X)
	case *ast.IndexListExpr:
		return receiverTypeName(t.X)
	default:
		return ""
	}
}

func genDeclName(d *ast.GenDecl) string {
	if len(d.Specs) == 0 {
		return ""
	}
	switch s := d.Specs[0].(type) {
	case *ast.TypeSpec:
		return s.Name.Name
	case *ast.ValueSpec:
		if len(s.Names) > 0 {
			return s.Names[0].Name
		}
	}
	return ""
}

type subChunk struct {
	start, end int
	content    string
}

func splitLongFunction(lines []string, start, end, maxLines int) []subChunk {
	const overlap = 5
	var chunks []subChunk
	cur := start
	for cur <= end {
		chunkEnd := cur + maxLines - 1
		if chunkEnd > end {
			chunkEnd = end
		}
		if chunkEnd < end {
			best := chunkEnd
			for i := chunkEnd; i >= cur+maxLines/2; i-- {
				if i-1 >= 0 && i-1 < len(lines) && strings.TrimSpace(lines[i-1]) == "" {
					best = i
					break
				}
			}
			chunkEnd = best
		}
		chunks = append(chunks, subChunk{
			start:   cur,
			end:     chunkEnd,
			content: joinLines(lines, cur-1, chunkEnd),
		})
		next := chunkEnd - overlap + 1
		if next <= cur {
			next = cur + 1
		}
		cur = next
		if chunkEnd >= end {
			break
		}
	}
	return chunks
}

func fallbackChunks(path, content string) []Chunk {
	return []Chunk{{
		StartLine: 1,
		EndLine:   strings.Count(content, "\n") + 1,
		ChunkType: TypeBlock,
		Content:   content,
	}}
}
