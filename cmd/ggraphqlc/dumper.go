package main

import (
	"fmt"
	"io"
	"path/filepath"
	"strconv"
	"strings"
	"unicode"
)

type writer struct {
	extractor
	out    io.Writer
	indent int
}

func write(extractor extractor, out io.Writer) {
	wr := writer{extractor, out, 0}

	wr.writePackage()
	wr.writeImports()
	wr.writeInterface()
	wr.writeVars()
	for _, object := range wr.Objects {
		wr.writeObjectResolver(object)
	}
	wr.writeSchema()
	wr.importHack()
}

func (w *writer) emit(format string, args ...interface{}) {
	io.WriteString(w.out, fmt.Sprintf(format, args...))
}

func (w *writer) emitIndent() {
	io.WriteString(w.out, strings.Repeat("	", w.indent))
}

func (w *writer) begin(format string, args ...interface{}) {
	w.emitIndent()
	w.emit(format, args...)
	w.lf()
	w.indent++
}

func (w *writer) end(format string, args ...interface{}) {
	w.indent--
	w.emitIndent()
	w.emit(format, args...)
	w.lf()
}

func (w *writer) line(format string, args ...interface{}) {
	w.emitIndent()
	w.emit(format, args...)
	w.lf()
}

func (w *writer) lf() {
	w.out.Write([]byte("\n"))
}

func (w *writer) writePackage() {
	w.line("package %s", w.PackageName)
	w.lf()
}

func (w *writer) writeImports() {
	w.begin("import (")
	for local, pkg := range w.Imports {
		if local == filepath.Base(pkg) {
			w.line(strconv.Quote(pkg))
		} else {
			w.line("%s %s", local, strconv.Quote(pkg))
		}

	}
	w.end(")")
	w.lf()
}

func (w *writer) writeInterface() {
	w.begin("type Resolvers interface {")
	for _, o := range w.Objects {
		for _, f := range o.Fields {
			if f.VarName != "" || f.MethodName != "" {
				continue
			}

			w.emitIndent()
			w.emit("%s_%s(", o.Name, f.GraphQLName)

			w.emit("ctx context.Context")
			if o.Type.Name != "interface{}" {
				w.emit(", it *%s", o.Type.Local())
			}
			for _, arg := range f.Args {
				w.emit(", %s %s", arg.Name, arg.Type.Local())
			}
			w.emit(") (%s, error)", f.Type.Local())
			w.lf()
		}
	}
	w.end("}")
	w.lf()
}

func (w *writer) writeVars() {
	w.begin("var (")
	for _, o := range w.Objects {
		satisfies := strconv.Quote(o.Type.GraphQLName)
		for _, s := range o.satisfies {
			satisfies += ", " + strconv.Quote(s)
		}
		w.line("%sSatisfies = []string{%s}", lcFirst(o.Type.GraphQLName), satisfies)
	}
	w.end(")")
	w.lf()
}

func (w *writer) writeObjectResolver(object object) {
	w.begin("func _%s(ec *executionContext, sel []query.Selection, it *%s) jsonw.Encodable {", lcFirst(object.Type.GraphQLName), object.Type.Local())

	w.line("groupedFieldSet := ec.collectFields(sel, %sSatisfies, map[string]bool{})", lcFirst(object.Type.GraphQLName))
	w.line("resultMap := jsonw.Map{}")
	w.begin("for _, field := range groupedFieldSet {")
	w.line("switch field.Name {")

	for _, field := range object.Fields {
		w.begin("case %s:", strconv.Quote(field.GraphQLName))

		if field.VarName != "" {
			w.writeEvaluateVar(field)
		} else {
			w.writeEvaluateMethod(object, field)
		}

		w.writeJsonType("json", field.Type, "res")
		w.line("resultMap.Set(field.Alias, json)")

		w.line("continue")
		w.end("")
	}
	w.line("}")
	w.line(`panic("unknown field " + strconv.Quote(field.Name))`)
	w.end("}")
	w.line("return resultMap")

	w.end("}")
	w.lf()
}

func (w *writer) writeEvaluateVar(field Field) {
	w.line("res := %s", field.VarName)
}

func (w *writer) writeEvaluateMethod(object object, field Field) {
	var methodName string
	if field.MethodName != "" {
		methodName = field.MethodName
	} else {
		methodName = fmt.Sprintf("ec.resolvers.%s_%s", object.Name, field.GraphQLName)
	}

	if field.NoErr {
		w.emitIndent()
		w.emit("res := %s", methodName)
		w.writeFuncArgs(object, field)
	} else {
		w.emitIndent()
		w.emit("res, err := %s", methodName)
		w.writeFuncArgs(object, field)
		w.line("if err != nil {")
		w.line("	ec.Error(err)")
		w.line("	continue")
		w.line("}")
	}
}

func (w *writer) writeFuncArgs(object object, field Field) {
	if len(field.Args) == 0 && field.MethodName != "" {
		w.emit("()")
		w.lf()
	} else {
		w.indent++
		w.emit("(")
		w.lf()
		if field.MethodName == "" {
			w.line("ec.ctx,")
			if object.Type.Name != "interface{}" {
				w.line("it,")
			}
		}
		for _, arg := range field.Args {
			w.line("field.Args[%s].(%s),", strconv.Quote(arg.Name), arg.Type.Local())
		}
		w.end(")")
	}
}

func (w *writer) writeJsonType(result string, t Type, val string) {
	w.doWriteJsonType(result, t, val, t.Modifiers, false)
}

func (w *writer) doWriteJsonType(result string, t Type, val string, remainingMods []string, isPtr bool) {
	for i := 0; i < len(remainingMods); i++ {
		switch remainingMods[i] {
		case modPtr:
			w.line("var %s jsonw.Encodable = jsonw.Null", result)
			w.begin("if %s != nil {", val)
			w.doWriteJsonType(result+"1", t, val, remainingMods[i+1:], true)
			w.line("%s = %s", result, result+"1")
			w.end("}")
			return
		case modList:
			if isPtr {
				val = "*" + val
			}
			w.line("%s := jsonw.Array{}", result)
			w.begin("for _, val := range %s {", val)

			w.doWriteJsonType(result+"1", t, "val", remainingMods[i+1:], false)
			w.line("%s = append(%s, %s)", result, result, result+"1")
			w.end("}")
			return
		}
	}

	if t.Basic {
		if isPtr {
			val = "*" + val
		}
		w.line("%s := jsonw.%s(%s)", result, ucFirst(t.Name), val)
	} else if len(t.Implementors) > 0 {
		w.line("var %s jsonw.Encodable = jsonw.Null", result)
		w.line("switch it := %s.(type) {", val)
		w.line("case nil:")
		w.line("	%s = jsonw.Null", result)
		for _, implementor := range t.Implementors {
			w.line("case %s:", implementor.Local())
			w.line("	%s = _%s(ec, field.Selections, &it)", result, lcFirst(implementor.GraphQLName))
			w.line("case *%s:", implementor.Local())
			w.line("	%s = _%s(ec, field.Selections, it)", result, lcFirst(implementor.GraphQLName))
		}

		w.line("default:")
		w.line(`	panic(fmt.Errorf("unexpected type %%T", it))`)
		w.line("}")
	} else {
		if !isPtr {
			val = "&" + val
		}
		w.line("%s := _%s(ec, field.Selections, %s)", result, lcFirst(t.GraphQLName), val)
	}
}

func ucFirst(s string) string {
	r := []rune(s)
	r[0] = unicode.ToUpper(r[0])
	return string(r)
}

func lcFirst(s string) string {
	r := []rune(s)
	r[0] = unicode.ToLower(r[0])
	return string(r)
}

func (w *writer) writeSchema() {
	w.line("var parsedSchema = schema.MustParse(%s)", strconv.Quote(w.schemaRaw))
}

func (w *writer) importHack() {
	w.line("var _ = fmt.Print")
}