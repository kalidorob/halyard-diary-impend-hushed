// Similar to find-embeddings.go, but uses the go/analysis framework.
//
// To use this tool, it's recommended to 'go build' it first and then invoke
// it from the root directly of the module you want to analyze, with the package
// patter as the sole argument; for example:
//
// $ find-embeddings-analysis ./...
//
// Run with -help to see the flags inherited from the go/analysis framework.
//
// Eli Bendersky [https://eli.thegreenplace.net]
// This code is in the public domain.
package main

import (
	"bytes"
	"fmt"
	"go/ast"
	"go/format"
	"go/token"
	"regexp"
	"strings"
	"sync"

	"golang.org/x/tools/go/analysis"
	"golang.org/x/tools/go/analysis/singlechecker"

	"github.com/pkg/errors"
)

var mux, muxEC sync.Mutex
var commentStrip = regexp.MustCompile("^[ \t]*//[ \t]*")

var EmbedAnalysis = &analysis.Analyzer{
	Name: "embedanalysis",
	Doc:  "reports embeddings",
	Run:  run,
}

var rabbitEventTypes map[string]string
var rabbitEmitterCalls map[string]string

func main() {
	rabbitEventTypes = make(map[string]string)
	rabbitEmitterCalls = make(map[string]string)

	singlechecker.Main(EmbedAnalysis)
}

func run(pass *analysis.Pass) (interface{}, error) {
	fmt.Printf("==> PASS ==> %v\n", pass)

	for _, file := range pass.Files {
		emitters := make(map[string]string)

		ast.Inspect(file, func(n ast.Node) bool {
			fmt.Printf("%T %v\n", n, n)
			if c, ok := n.(*ast.CommentGroup); ok {
				fmt.Printf("==> %v\n", c)
			}
			if as, ok := n.(*ast.AssignStmt); ok {
				fmt.Printf("AIT: %v\n", as)
			}
			if kve, ok := n.(*ast.KeyValueExpr); ok {
				if i, ok := kve.Key.(*ast.Ident); ok {
					if v, ok := kve.Value.(*ast.CallExpr); ok {
						fi, fse, err := selectorParts(v.Fun)
						if err == nil {
							if len(v.Args) > 0 {
								ai, ase, err := selectorParts(v.Args[0])
								if err == nil {
									fmt.Printf("KVE: %s %s.%s %s.%s\n", i.Name, fi, fse, ai, ase)
									fmt.Printf("emitter: (%s) => (%s.%s)\n", i.Name, ai, ase)
									emitters[i.Name] = ai + "." + ase
								}
							}
						}

					}
				}
			}
			if ce, ok := n.(*ast.CallExpr); ok {
				//				fmt.Printf("F: %v\nA: %v\n", ce.Fun, ce.Args)
				fi, fse, err := selectorParts(ce.Fun)
				if err == nil {
					fmt.Printf("CALL %s.%s\n", fi, fse)
					if len(ce.Args) > 0 {
						fmt.Printf("LAST ARG %s.%s: %T\n", fi, fse, ce.Args[len(ce.Args)-1])
						if ai, ok := ce.Args[len(ce.Args)-1].(*ast.Ident); ok {
							if ai.Obj == nil {
								fmt.Printf("%s.%s OBJ is nil for some reason.", fi, fse)
							}
							fmt.Printf("%s -> %p\n", fse, ai.Obj)
							t, err := typeFromObj(ai.Obj, fse)
							if err == nil {
								if v, ok := emitters[fse]; ok {
									fmt.Printf("checkemitter: %s.%s => %s => %s L= %d\n", fi, fse, t, v, pass.Fset.Position(ce.Lparen).Line)
									muxEC.Lock()
									rabbitEmitterCalls[v] = t
									muxEC.Unlock()
								}
							}
						}
					}
				}
			}
			if f, ok := n.(*ast.Field); ok {
				if len(f.Names) > 0 {
					fmt.Printf("FIELD N=%s T=%s t=%T\n", f.Names[0].Name, f.Type, f.Type)
					i, se, err := selectorParts(f.Type)
					if err == nil {
						if i == "rabbitEvents" && se == "EventEmitter" {
							fmt.Printf("Found an emitter: %s\n", f.Names[0].Name)
						}
					}
				}
				//fmt.Printf("XXXF %v\n", f)
			}
			if g, ok := n.(*ast.GenDecl); ok {
				if g.Tok == token.CONST {
					//					fmt.Printf("const: pos=%d\n", g.TokPos)
					for _, x := range g.Specs {
						if q, ok := x.(*ast.ValueSpec); ok {
							if q.Values != nil {
								if b, ok := q.Values[0].(*ast.BasicLit); ok {
									hint := "types.UnknownEventType"
									if q.Comment != nil {
										hint = commentStrip.ReplaceAllString(q.Comment.List[0].Text, "")
									}
									if strings.HasPrefix(q.Names[0].Name, "Event") {
										fmt.Printf("emitter const= %s.%s event= %s type= %s\n", pass.Pkg.Name(), q.Names[0].Name, b.Value, hint)
										mux.Lock()
										rabbitEventTypes["types."+q.Names[0].Name] = hint
										mux.Unlock()
									}
								}
							}
						}
					}
					/*
						if g.Doc != nil {
							for _, y := range g.Doc.List {
								//	fmt.Printf("// %s\n", y.Text)
							}
						}
					*/
				}
			}

			return true
		})
	}
	return nil, nil
}

func reportMatches(pass *analysis.Pass) (interface{}, error) {
	fmt.Println("And now for something completely different...")
	fmt.Printf("==> PASS ==> %v\n", pass)

	for k, v := range rabbitEventTypes {
		fmt.Printf("RET: %s => %s\n", k, v)
	}

	for k, v := range rabbitEmitterCalls {
		fmt.Printf("REC: %s => %s\n", k, v)
	}

	return nil, nil
}

// nodeString formats a syntax tree in the style of gofmt.
func nodeString(n ast.Node, fset *token.FileSet) string {
	var buf bytes.Buffer
	format.Node(&buf, fset, n)
	return buf.String()
}

func selectorParts(sel interface{}) (string, string, error) {
	if se, ok := sel.(*ast.SelectorExpr); ok {
		if i, ok := se.X.(*ast.Ident); ok {
			return i.Name, se.Sel.Name, nil
		}
	}
	return "", "", errors.New("bork")
}

func typeFromObj(o *ast.Object, tag string) (string, error) {
	if o != nil {
		ei, ese := "pkg-"+tag, "sel-"+tag
		ei = fmt.Sprintf("pkg-%s-%T\n", tag, o.Decl)
		if f, ok := o.Decl.(*ast.FuncDecl); ok {
			if f.Type.Results != nil {
				if len(f.Type.Results.List) > 0 {
					r := f.Type.Results.List[0]
					//					if t, ok := r.Type
					fmt.Printf("%s => %T %T\n", tag, r, r.Type)
					if st, ok := r.Type.(*ast.StarExpr); ok {
						sti, stse, err := selectorParts(st.X)
						if err == nil {
							fmt.Printf("%s ASSIGN\n", tag)
							ei, ese = sti, stse
						}
					}
				}
			}
		}
		if f, ok := o.Decl.(*ast.Field); ok {
			if st, ok := f.Type.(*ast.StarExpr); ok {
				sti, stse, err := selectorParts(st.X)
				if err == nil {
					fmt.Printf("%s ASTFIELD\n", tag)
					ei, ese = sti, stse
				}
			}
		}
		if f, ok := o.Decl.(*ast.AssignStmt); ok {
			if l, ok := f.Lhs[0].(*ast.Ident); ok {
				_ = l
				fmt.Printf("0===> %s %T\n", tag, f.Rhs[0])
				if r, ok := f.Rhs[0].(*ast.Ident); ok {
					q, err := typeFromObj(r.Obj, tag+"-rhs")
					if err == nil {
						fmt.Printf("1===> %s RHS %s\n", tag, q)
					}
				}
				if ce, ok := f.Rhs[0].(*ast.CallExpr); ok {
					fi, fse, err := selectorParts(ce.Fun)
					if err == nil {
						fmt.Printf("2===> %s RHS %s.%s %T\n", tag, fi, fse, ce.Fun)

					} else {
						if i, ok := ce.Fun.(*ast.Ident); ok {
							t, err := typeFromObj(i.Obj, tag+"-rhs-obj")
							if err == nil {
								fmt.Printf("3===> %s RHS %T %v %s %s\n", tag, ce.Fun, ce.Fun, i.Name, t)
								return t, nil
							}
						}

					}
				}
			}
		}
		return ei + "." + ese, nil
	}
	return "", errors.New("typeFromObj")
}
