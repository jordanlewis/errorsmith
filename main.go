package main

import (
	"bytes"
	"flag"
	"fmt"
	"go/ast"
	"go/format"
	"go/parser"
	"go/token"
	"io/ioutil"
	"log"
	"os"

	"github.com/pkg/errors"
)

const usageMessage = "" +
	`Usage of 'errorsmith':

Randomly inject errors into a Go file:
    errorsmith file.go
`

func usage() {
	fmt.Fprintln(os.Stderr, usageMessage)
	fmt.Fprintln(os.Stderr, "Flags:")
	flag.PrintDefaults()
	os.Exit(2)
}

var output = flag.String("o", "", "file for output; default: stdout")
var errorPercent = flag.Float64("error-percent", 0.1, "percent error likelihood")

const (
	randPackagePath = "math/rand"
	randPackageName = "_errorsmith_rand_"

	fmtPackagePath = "fmt"
	fmtPackageName = "_errorsmith_fmt_"
)

func main() {
	flag.Usage = usage
	flag.Parse()

	// Usage information when no arguments.
	if flag.NFlag() == 0 && flag.NArg() == 0 {
		flag.Usage()
	}
	injectErrors(flag.Arg(0))
	return
}

// File is a wrapper for the state of a file used in the parser.
// The basic parse tree walker is a method of this type.
type File struct {
	fset    *token.FileSet
	name    string // Name of file.
	astFile *ast.File
	content []byte
	edit    *Buffer
}

// findText finds text in the original source, starting at pos.
// It correctly skips over comments and assumes it need not
// handle quoted strings.
// It returns a byte offset within f.src.
func (f *File) findText(pos token.Pos, text string) int {
	b := []byte(text)
	start := f.offset(pos)
	i := start
	s := f.content
	for i < len(s) {
		if bytes.HasPrefix(s[i:], b) {
			return i
		}
		if i+2 <= len(s) && s[i] == '/' && s[i+1] == '/' {
			for i < len(s) && s[i] != '\n' {
				i++
			}
			continue
		}
		if i+2 <= len(s) && s[i] == '/' && s[i+1] == '*' {
			for i += 2; ; i++ {
				if i+2 > len(s) {
					return 0
				}
				if s[i] == '*' && s[i+1] == '/' {
					i += 2
					break
				}
			}
			continue
		}
		i++
	}
	return -1
}

// Visit implements the ast.Visitor interface.
func (f *File) Visit(node ast.Node) ast.Visitor {
	switch n := node.(type) {
	case *ast.ImportSpec:
	case *ast.IfStmt:
		if n.Init != nil {
			ast.Walk(f, n.Init)
		}
		if n.Init == nil {
			// Can't inject faults into auto-initialized nils yet.
			if e, ok := n.Cond.(*ast.BinaryExpr); ok {
				if x, ok := e.X.(*ast.Ident); ok && x.Name == "err" {
					if e.Op == token.EQL || e.Op == token.NEQ {
						if y, ok := e.Y.(*ast.Ident); ok && y.Name == "nil" {
							// We found an if of form err == nil. Inject a fault!
							f.edit.Insert(f.offset(n.Pos()),
								fmt.Sprintf(`if %s.Int() %% %d == 0 {
    %s.Printf("injected error at %s:%d\n")
    err = %s.Errorf("injected error at %s:%d")
}
`, randPackageName, int(100/(*errorPercent)),
									fmtPackageName,
									f.name, f.fset.Position(n.Pos()).Line,
									fmtPackageName,
									f.name, f.fset.Position(n.Pos()).Line,
								))
						}
					}
				}
			}
		}
		ast.Walk(f, n.Cond)
		ast.Walk(f, n.Body)
		if n.Else == nil {
			return nil
		}
		// The elses are special, because if we have
		//	if x {
		//	} else if y {
		//	}
		// we want to cover the "if y". To do this, we need a place to drop the counter,
		// so we add a hidden block:
		//	if x {
		//	} else {
		//		if y {
		//		}
		//	}
		elseOffset := f.findText(n.Body.End(), "else")
		if elseOffset < 0 {
			panic("lost else")
		}
		f.edit.Insert(elseOffset+4, "{")
		f.edit.Insert(f.offset(n.Else.End()), "}")

		// We just created a block, now walk it.
		// Adjust the position of the new block to start after
		// the "else". That will cause it to follow the "{"
		// we inserted above.
		pos := f.fset.File(n.Body.End()).Pos(elseOffset + 4)
		switch stmt := n.Else.(type) {
		case *ast.IfStmt:
			block := &ast.BlockStmt{
				Lbrace: pos,
				List:   []ast.Stmt{stmt},
				Rbrace: stmt.End(),
			}
			n.Else = block
		case *ast.BlockStmt:
			stmt.Lbrace = pos
		default:
			panic("unexpected node type in if")
		}
		ast.Walk(f, n.Else)
		return nil
		return nil
	}
	return f
}

// offset translates a token position into a 0-indexed byte offset.
func (f *File) offset(pos token.Pos) int {
	return f.fset.Position(pos).Offset
}

func injectErrors(name string) {
	fset := token.NewFileSet()
	content, err := ioutil.ReadFile(name)
	if err != nil {
		log.Fatalf("errorsmith: %s: %s", name, err)
	}
	parsedFile, err := parser.ParseFile(fset, name, content, parser.ParseComments)
	if err != nil {
		log.Fatalf("errorsmith: %s: %s", name, err)
	}

	file := &File{
		fset:    fset,
		name:    name,
		content: content,
		edit:    NewBuffer(content),
		astFile: parsedFile,
	}
	file.edit.Insert(file.offset(file.astFile.Name.End()),
		fmt.Sprintf(`
import %s %q
import %s %q
`,
			randPackageName, randPackagePath,
			fmtPackageName, fmtPackagePath,
		))

	ast.Walk(file, file.astFile)
	newContent := file.edit.Bytes()
	newContent = append(newContent, []byte(fmt.Sprintf("\nvar _ = %s.Int", randPackageName))...)
	newContent = append(newContent, []byte(fmt.Sprintf("\nvar _ = %s.Printf", fmtPackageName))...)

	fd := os.Stdout
	if *output != "" {
		var err error
		fd, err = os.Create(*output)
		if err != nil {
			log.Fatalf("errorsmith: %s", err)
		}
	}

	formatted, err := format.Source(newContent)
	if err != nil {
		// Write out incorrect source for easier debugging.
		formatted = newContent
		err = errors.Wrap(err, "Code formatting failed with Go parse error")
	}
	fd.Write(formatted)

	if err != nil {
		log.Fatalf("errorsmith: %s", err)
	}
}
