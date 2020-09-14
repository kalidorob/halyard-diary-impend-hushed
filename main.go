package main

import (
	"fmt"
	"go/ast"
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

var EmitterAnalysis = &analysis.Analyzer{
	Name: "emitteranalysis",
	Doc:  "reports emitter types and stuff",
	Run:  run,
}

// RUNNING THIS CLUNKMEISTER:
//
// ```
// cd $HOME/git/kpc
// ./whatevs ./types/... | grep 'emitter const' | awk '{print $3,$7}' | sort -u > ET1
// ./whatevs ./services/... | grep checkem | awk '{print $6,$4,$2}' | sort -u > ET2
// join ET1 ET2 | awk 'NF==4 && $2!=$3 {print $4" emits "$3" but "$1" wants "$2}'
// ```
//
// There's no way to run anything after `singlechecker.Main` because it calls `os.Exit`.
// You can't get around it using `multichecker` because that intersperses the analyses.
// My original plan was to collect all the types and the calls into maps and then join
// them together at the end - package passes are run in arbitrary order in goroutines -
// which means you can't guarantee seeing the type you want before the call it's used in.
// I may well drop the `analysis` route and hack up a "create the fileset and parse"
// but whilst we're still evaluating the worth of this POC, we can be clunky.

// FLAWS:
//
// Doesn't catch the type of `settings` here:
// `err = s.userEvent(rabbitEvents.Create, md, auth.UserID, nil, settings)`
// because it's created by a call:
// `settings, err := s.updateUserSettings("CreateUserAccountSettings", auth.UserID, req)`
// and we don't yet capture those return types.

func main() {
	singlechecker.Main(EmitterAnalysis)
}

func run(pass *analysis.Pass) (interface{}, error) {
	// fmt.Printf("==> PASS ==> %v\n", pass)

	for _, file := range pass.Files {
		emitters := make(map[string]string)

		ast.Inspect(file, func(n ast.Node) bool {
			// Uncomment this for when you can't figure out wth something is going to be.
			// fmt.Printf("%T %v\n", n, n)

			// Our emitter functions are defined thusly:
			// `userEvent:   rabbitEvents.Emit(types.EventPathUserAccountSettings)`
			// which means if we have a struct field that's created by calling
			// `rabbitEvents.Emit`, the argument is our constant.
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
									// Remember the mapping of emitter name to emission type.
									emitters[i.Name] = ai + "." + ase
								}
							}
						}

					}
				}
			}

			// We wmit events by calling our emitter which means we need to find selector
			// calls where the method matches one of our known emitters.
			// ie `err = s.userEvent(rabbitEvents.Create, md, auth.UserID, nil, settings)`
			// since we've already seen `userEvent` being typed as `EventEmitter`, this is us.
			if ce, ok := n.(*ast.CallExpr); ok {
				fi, fse, err := selectorParts(ce.Fun)
				if err == nil {
					// fmt.Printf("CALL %s.%s\n", fi, fse)
					if len(ce.Args) > 0 {
						// fmt.Printf("LAST ARG %s.%s: %T\n", fi, fse, ce.Args[len(ce.Args)-1])
						if ai, ok := ce.Args[len(ce.Args)-1].(*ast.Ident); ok {
							if ai.Obj == nil {
								fmt.Printf("%s.%s OBJ is nil for some reason.", fi, fse)
							}
							// fmt.Printf("%s -> %p\n", fse, ai.Obj)
							t, err := typeFromObj(ai.Obj, fse)
							if err == nil {
								if v, ok := emitters[fse]; ok {
									fmt.Printf("checkemitter: %s.%s => %s => %s L= %d\n", fi, fse, t, v, pass.Fset.Position(ce.Lparen).Line)
								}
							}
						}
					}
				}
			}

			// If we have a struct field of type `rabbitEvents.EventEmitter`, that's
			// going to be the name of our emitter later.  Although we don't currently
			// do anything with this information right now...
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
			}

			// For our constants, we're looking for lines matching this kind of pattern.
			// `const XYZ = "blah.blah" // pkg.type`
			if g, ok := n.(*ast.GenDecl); ok {
				if g.Tok == token.CONST {
					// fmt.Printf("const: pos=%d\n", g.TokPos)
					for _, x := range g.Specs {
						if q, ok := x.(*ast.ValueSpec); ok {
							if q.Values != nil {
								// If the first value is a `BasicLit`, it might be our string.
								if b, ok := q.Values[0].(*ast.BasicLit); ok {
									hint := "types.UnknownEventType"
									// The comment is the type hint we're ultimately after.
									if q.Comment != nil {
										hint = commentStrip.ReplaceAllString(q.Comment.List[0].Text, "")
									}
									// We only want constants beginning with `Event`.
									if strings.HasPrefix(q.Names[0].Name, "Event") {
										fmt.Printf("emitter const= %s.%s event= %s type= %s\n", pass.Pkg.Name(), q.Names[0].Name, b.Value, hint)
									}
								}
							}
						}
					}
				}
			}

			return true
		})
	}
	return nil, nil
}

func selectorParts(sel interface{}) (string, string, error) {
	if se, ok := sel.(*ast.SelectorExpr); ok {
		if i, ok := se.X.(*ast.Ident); ok {
			return i.Name, se.Sel.Name, nil
		}
	}
	return "", "", errors.New("bork")
}

// This is horrible. I can only apologise but this is what AST forces you into.
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
					// fmt.Printf("%s ASTFIELD\n", tag)
					ei, ese = sti, stse
				}
			}
		}
		if f, ok := o.Decl.(*ast.AssignStmt); ok {
			if l, ok := f.Lhs[0].(*ast.Ident); ok {
				_ = l
				// fmt.Printf("0===> %s %T\n", tag, f.Rhs[0])
				if r, ok := f.Rhs[0].(*ast.Ident); ok {
					q, err := typeFromObj(r.Obj, tag+"-rhs")
					if err == nil {
						// fmt.Printf("1===> %s RHS %s\n", tag, q)
						_ = q
					}
				}
				if ce, ok := f.Rhs[0].(*ast.CallExpr); ok {
					fi, fse, err := selectorParts(ce.Fun)
					if err == nil {
						// fmt.Printf("2===> %s RHS %s.%s %T\n", tag, fi, fse, ce.Fun)
						_, _ = fi, fse
					} else {
						if i, ok := ce.Fun.(*ast.Ident); ok {
							t, err := typeFromObj(i.Obj, tag+"-rhs-obj")
							if err == nil {
								// fmt.Printf("3===> %s RHS %T %v %s %s\n", tag, ce.Fun, ce.Fun, i.Name, t)
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
