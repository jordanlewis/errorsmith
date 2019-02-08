package main

import (
	"flag"
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"io/ioutil"
	"log"
	"os"
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

const (
	randPackagePath = "math/rand"
	randPackageName = "_errorsmith_rand_"
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

// Block represents the information about a basic block to be recorded in the analysis.
// Note: Our definition of basic block is based on control structures; we don't break
// apart && and ||. We could but it doesn't seem important enough to bother.
type Block struct {
	startByte token.Pos
	endByte   token.Pos
	numStmt   int
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

// Visit implements the ast.Visitor interface.
func (f *File) Visit(node ast.Node) ast.Visitor {
	switch n := node.(type) {
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
								fmt.Sprintf(`if %s.Int() %% 8 == 0 {
    err = errors.New("injected error at %s:%d")
}
`, randPackageName, f.name, f.fset.Position(n.Pos()).Line))
						}
					}
				}
			}
		}
		ast.Walk(f, n.Cond)
		ast.Walk(f, n.Body)
		if n.Else != nil {
			ast.Walk(f, n.Else)
		}
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
		fmt.Sprintf("; import %s %q", randPackageName, randPackagePath))

	ast.Walk(file, file.astFile)
	newContent := file.edit.Bytes()

	fd := os.Stdout
	if *output != "" {
		var err error
		fd, err = os.Create(*output)
		if err != nil {
			log.Fatalf("errorsmith: %s", err)
		}
	}

	fd.Write(newContent)
}
