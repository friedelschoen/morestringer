package main

import (
	"bytes"
	"fmt"
	"go/format"
	"log"
	"sort"
	"strings"
)

// usize returns the number of bits of the smallest unsigned integer
// type that will hold n. Used to create the smallest possible slice of
// integers to use as indexes into the concatenated strings.
func usize(n int) int {
	switch {
	case n < 1<<8:
		return 8
	case n < 1<<16:
		return 16
	default:
		// 2^32 is enough constants for anyone.
		return 32
	}
}

// splitIntoRuns breaks the values into runs of contiguous sequences.
// For example, given 1,2,3,5,6,7 it returns {1,2,3},{5,6,7}.
// The input slice is known to be non-empty.
func splitIntoRuns(values []Value) [][]Value {
	// We use stable sort so the lexically first name is chosen for equal elements.
	sort.Stable(byValue(values))
	// Remove duplicates. Stable sort has put the one we want to print first,
	// so use that one. The String method won't care about which named constant
	// was the argument, so the first name for the given value is the only one to keep.
	// We need to do this because identical values would cause the switch or map
	// to fail to compile.
	j := 1
	for i := 1; i < len(values); i++ {
		if values[i].value != values[i-1].value {
			values[j] = values[i]
			j++
		}
	}
	values = values[:j]
	runs := make([][]Value, 0, 10)
	for len(values) > 0 {
		// One contiguous sequence per outer loop.
		i := 1
		for i < len(values) && values[i].value == values[i-1].value+1 {
			i++
		}
		runs = append(runs, values[:i])
		values = values[i:]
	}
	return runs
}

func fnv1a32(s string) uint32 {
	var h uint32 = 2166136261
	for i := 0; i < len(s); i++ {
		h ^= uint32(s[i])
		h *= 16777619
	}
	return h
}

// Generator holds the state of the analysis. Primarily used to buffer
// the output for format.Source.
type Generator struct {
	buf bytes.Buffer // Accumulated output.
	pkg *Package     // Package we are scanning.

	logf   func(format string, args ...any) // test logging hook; nil when not testing
	lookup string
}

func (g *Generator) Printf(format string, args ...any) {
	fmt.Fprintf(&g.buf, format, args...)
}

// format returns the gofmt-ed contents of the Generator's buffer.
func (g *Generator) format() []byte {
	src, err := format.Source(g.buf.Bytes())
	if err != nil {
		// Should never happen, but can arise when developing this code.
		// The user can compile the output to see the error.
		log.Printf("warning: internal error: invalid Go generated: %s", err)
		log.Printf("warning: compile the package to analyze the error")
		return g.buf.Bytes()
	}
	return src
}

// declareIndexAndNameVars declares the index slices and concatenated names
// strings representing the runs of values.
func (g *Generator) declareIndexAndNameVars(runs [][]Value, typeName string) {
	var indexes, names []string
	for i, run := range runs {
		index, name := g.createIndexAndNameDecl(run, typeName, fmt.Sprintf("_%d", i))
		if len(run) != 1 {
			indexes = append(indexes, index)
		}
		names = append(names, name)
	}
	g.Printf("const (\n")
	for _, name := range names {
		g.Printf("%s\n", name)
	}
	g.Printf(")\n\n")

	if len(indexes) > 0 {
		g.Printf("var (")
		for _, index := range indexes {
			g.Printf("%s\n", index)
		}
		g.Printf(")\n\n")
	}
}

// declareIndexAndNameVar is the single-run version of declareIndexAndNameVars
func (g *Generator) declareIndexAndNameVar(run []Value, typeName string) {
	index, name := g.createIndexAndNameDecl(run, typeName, "")
	g.Printf("const %s\n", name)
	g.Printf("var %s\n", index)
}

// createIndexAndNameDecl returns the pair of declarations for the run. The caller will add "const" and "var".
func (g *Generator) createIndexAndNameDecl(run []Value, typeName string, suffix string) (string, string) {
	b := new(bytes.Buffer)
	indexes := make([]int, len(run))
	for i := range run {
		b.WriteString(run[i].name)
		indexes[i] = b.Len()
	}
	nameConst := fmt.Sprintf("_%s_name%s = %q", typeName, suffix, b.String())
	nameLen := b.Len()
	b.Reset()
	fmt.Fprintf(b, "_%s_index%s = [...]uint%d{0, ", typeName, suffix, usize(nameLen))
	for i, v := range indexes {
		if i > 0 {
			fmt.Fprintf(b, ", ")
		}
		fmt.Fprintf(b, "%d", v)
	}
	fmt.Fprintf(b, "}")
	return b.String(), nameConst
}

// declareNameVars declares the concatenated names string representing all the values in the runs.
func (g *Generator) declareNameVars(runs [][]Value, typeName string, suffix string) {
	g.Printf("const _%s_name%s = \"", typeName, suffix)
	for _, run := range runs {
		for i := range run {
			g.Printf("%s", run[i].name)
		}
	}
	g.Printf("\"\n")
}

// generate produces the String method for the named type.
func (g *Generator) generate(typeName string, values []Value) {
	g.buildCheck(values)
	if g.lookup != "" {
		// For each value, you'll get 4 lines of source-code. This
		// might overfloat the resulting file and we're choosing to
		// generate a less verbose technique.
		switch n := len(values); {
		case n <= 500:
			g.buildLookup(typeName, values) // fnv32 hash-switch
		case n <= 5000:
			g.buildLookupBinary(typeName, values) // binary search op gesorteerde namen
		default:
			g.buildLookupMap(typeName, values) // map
		}
	}
	runs := splitIntoRuns(values)

	// The decision of which pattern to use depends on the number of
	// runs in the numbers. If there's only one, it's easy. For more than
	// one, there's a tradeoff between complexity and size of the data
	// and code vs. the simplicity of a map. A map takes more space,
	// but so does the code. The decision here (crossover at 10) is
	// arbitrary, but considers that for large numbers of runs the cost
	// of the linear scan in the switch might become important, and
	// rather than use yet another algorithm such as binary search,
	// we punt and use a map. In any case, the likelihood of a map
	// being necessary for any realistic example other than bitmasks
	// is very low. And bitmasks probably deserve their own analysis,
	// to be done some other day.
	switch {
	case len(runs) == 1:
		g.buildOneRun(runs, typeName)
	case len(runs) <= 10:
		g.buildMultipleRuns(runs, typeName)
	default:
		g.buildMap(runs, typeName)
	}
}

func (g *Generator) buildCheck(values []Value) {
	// Generate code that will fail if the constants change value.
	g.Printf("func _() {\n")
	g.Printf("// An \"invalid array index\" compiler error signifies that the constant values have changed.\n")
	g.Printf("// Re-run the stringer command to generate them again.\n")
	g.Printf("var x [1]struct{}\n")
	for _, v := range values {
		g.Printf("_ = x[%s - %s]\n", v.originalName, v.str)
	}
	g.Printf("}\n")
}

// buildOneRun generates the variables and String method for a single run of contiguous values.
func (g *Generator) buildOneRun(runs [][]Value, typeName string) {
	values := runs[0]
	g.Printf("\n")
	g.declareIndexAndNameVar(values, typeName)
	g.Printf(stringOneRun, typeName, values[0].String())
}

// Arguments to format are:
//
//	[1]: type name
//	[2]: lowest defined value for type, as a string
const stringOneRun = `func (i %[1]s) String() string {
	idx := int(i) - %[2]s
	if i < %[2]s || idx >= len(_%[1]s_index)-1 {
		return "%[1]s(" + strconv.FormatInt(int64(i), 10) + ")"
	}
	return _%[1]s_name[_%[1]s_index[idx] : _%[1]s_index[idx+1]]
}
`

// buildMultipleRuns generates the variables and String method for multiple runs of contiguous values.
// For this pattern, a single Printf format won't do.
func (g *Generator) buildMultipleRuns(runs [][]Value, typeName string) {
	g.Printf("\n")
	g.declareIndexAndNameVars(runs, typeName)
	g.Printf("func (i %s) String() string {\n", typeName)
	g.Printf("switch {\n")
	for i, values := range runs {
		if len(values) == 1 {
			g.Printf("case i == %s:\n", &values[0])
			g.Printf("return _%s_name_%d\n", typeName, i)
			continue
		}
		if values[0].value == 0 && !values[0].signed {
			// For an unsigned lower bound of 0, "0 <= i" would be redundant.
			g.Printf("case i <= %s:\n", &values[len(values)-1])
		} else {
			g.Printf("case %s <= i && i <= %s:\n", &values[0], &values[len(values)-1])
		}
		if values[0].value != 0 {
			g.Printf("i -= %s\n", &values[0])
		}
		g.Printf("return _%s_name_%d[_%s_index_%d[i]:_%s_index_%d[i+1]]\n",
			typeName, i, typeName, i, typeName, i)
	}
	g.Printf("default:\n")
	g.Printf("return \"%s(\" + strconv.FormatInt(int64(i), 10) + \")\"\n", typeName)
	g.Printf("}\n")
	g.Printf("}\n")
}

// buildMap handles the case where the space is so sparse a map is a reasonable fallback.
// It's a rare situation but has simple code.
func (g *Generator) buildMap(runs [][]Value, typeName string) {
	g.Printf("\n")
	g.declareNameVars(runs, typeName, "")
	g.Printf("\nvar _%s_map = map[%s]string{\n", typeName, typeName)
	n := 0
	for _, values := range runs {
		for _, value := range values {
			g.Printf("%s: _%s_name[%d:%d],\n", &value, typeName, n, n+len(value.name))
			n += len(value.name)
		}
	}
	g.Printf("}\n\n")
	g.Printf(stringMap, typeName)
}

func (g *Generator) buildLookup(typeName string, values []Value) {
	g.Printf("\n")

	funcName := strings.Replace(g.lookup, "{}", typeName, 1)
	g.Printf("func %s(name string) (%s, bool) {\n", funcName, typeName)
	g.Printf("//fnv1a32 hash\n")
	g.Printf("var h uint32 = 2166136261\n")
	g.Printf("for i := 0; i < len(name); i++ {\n")
	g.Printf("h ^= uint32(name[i])\n")
	g.Printf("h *= 16777619\n")
	g.Printf("}\n")
	g.Printf("\n")
	g.Printf("switch h {\n")

	// group by hash
	type entry struct {
		hash uint32
		name string
		val  string
	}
	ents := make([]entry, len(values))
	for i, v := range values {
		ents[i] = entry{
			hash: fnv1a32(v.name),
			name: v.name,
			val:  v.originalName,
		}
	}

	sort.Slice(ents, func(i, j int) bool {
		if ents[i].hash != ents[j].hash {
			return ents[i].hash < ents[j].hash
		}
		return ents[i].name < ents[j].name
	})

	// emit switch cases, handling collisions
	prev := uint32(0xffffffff)
	for _, ent := range ents {
		h := ent.hash
		if h != prev {
			g.Printf("case 0x%08x:\n", h)
			prev = h
		}
		g.Printf("if name == %q {\n", ent.name)
		g.Printf("return %s, true\n", ent.val)
		g.Printf("}\n")
	}

	g.Printf("}\n")
	g.Printf("return 0, false\n")
	g.Printf("}\n")
}

func (g *Generator) buildLookupBinary(typeName string, values []Value) {
	g.Printf("\n")

	// Kopieer en sorteer op de uiteindelijke naam (dus v.name).
	vs := append([]Value(nil), values...)
	sort.Slice(vs, func(i, j int) bool {
		if vs[i].name != vs[j].name {
			return vs[i].name < vs[j].name
		}
		// Stabieler/debugvriendelijker bij gelijke namen.
		return vs[i].originalName < vs[j].originalName
	})

	// Hergebruik bestaande compacte concat-string + index array generator.
	// Dit genereert:
	//   const _<T>_name_lookup = "..."
	//   var   _<T>_index_lookup = [...]uintN{...}
	indexDecl, nameDecl := g.createIndexAndNameDecl(vs, typeName, "_lookup")
	g.Printf("const %s\n", nameDecl)
	g.Printf("var %s\n", indexDecl)

	// Waarden in exact dezelfde volgorde als de gesorteerde namen.
	g.Printf("var _%s_value_lookup = [...]%s{\n", typeName, typeName)
	for _, v := range vs {
		g.Printf("%s,\n", v.originalName)
	}
	g.Printf("}\n\n")

	funcName := strings.Replace(g.lookup, "{}", typeName, 1)
	g.Printf("func %s(name string) (%s, bool) {\n", funcName, typeName)

	// hi is len(value_lookup); index array heeft len+1, maar die gebruiken we via mid+1.
	g.Printf("lo, hi := 0, len(_%s_value_lookup)\n", typeName)
	g.Printf("for lo < hi {\n")
	g.Printf("mid := int(uint(lo+hi) >> 1)\n")
	g.Printf("s := _%s_name_lookup[_%s_index_lookup[mid]:_%s_index_lookup[mid+1]]\n",
		typeName, typeName, typeName)

	g.Printf("if name == s {\n")
	g.Printf("return _%s_value_lookup[mid], true\n", typeName)
	g.Printf("}\n")
	g.Printf("if name < s {\n")
	g.Printf("hi = mid\n")
	g.Printf("} else {\n")
	g.Printf("lo = mid + 1\n")
	g.Printf("}\n")
	g.Printf("}\n")
	g.Printf("return 0, false\n")
	g.Printf("}\n")
}

func (g *Generator) buildLookupMap(typeName string, values []Value) {
	g.Printf("\n")

	g.Printf("var _%s_lookup = map[string]%s{\n", typeName, typeName)
	for _, v := range values {
		g.Printf("%q: %s,\n", v.name, v.originalName)
	}
	g.Printf("}\n")

	funcName := strings.Replace(g.lookup, "{}", typeName, 1)
	g.Printf("func %s(name string) (%s, bool) {\n", funcName, typeName)
	g.Printf("value, ok := _%s_lookup[name]\n", typeName)
	g.Printf("return value, ok\n")
	g.Printf("}\n")
}

// Argument to format is the type name.
const stringMap = `func (i %[1]s) String() string {
	if str, ok := _%[1]s_map[i]; ok {
		return str
	}
	return "%[1]s(" + strconv.FormatInt(int64(i), 10) + ")"
}
`
