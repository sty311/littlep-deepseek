package bridge

import (
	"strings"
	"unicode"
)

// The native latex renderer does not load mhchem. Translate a deliberately
// narrow, bounded subset of \ce into ordinary TeX for display only. Unknown
// syntax becomes escaped literal text; the model answer is never changed.
const (
	maxChemistryDisplayBytes = 8 * 1024
	maxChemistryBraceDepth   = 8
	maxChemistryGroupDepth   = 4
)

var chemistryElements = map[string]bool{
	"H": true, "He": true, "Li": true, "Be": true, "B": true, "C": true,
	"N": true, "O": true, "F": true, "Ne": true, "Na": true, "Mg": true,
	"Al": true, "Si": true, "P": true, "S": true, "Cl": true, "Ar": true,
	"K": true, "Ca": true, "Sc": true, "Ti": true, "V": true, "Cr": true,
	"Mn": true, "Fe": true, "Co": true, "Ni": true, "Cu": true, "Zn": true,
	"Ga": true, "Ge": true, "As": true, "Se": true, "Br": true, "Kr": true,
	"Rb": true, "Sr": true, "Ag": true, "Cd": true, "In": true, "Sn": true,
	"Sb": true, "Te": true, "I": true, "Xe": true, "Cs": true, "Ba": true,
	"W": true, "Pt": true, "Au": true, "Hg": true, "Pb": true, "Bi": true,
}

func normalizeChemistryLatex(formula string) string {
	if !strings.Contains(formula, `\ce`) {
		return formula
	}
	if len(formula) > maxChemistryDisplayBytes {
		return chemistryLiteral(formula)
	}
	var out strings.Builder
	for at := 0; at < len(formula); {
		index := strings.Index(formula[at:], `\ce`)
		if index < 0 {
			out.WriteString(formula[at:])
			break
		}
		index += at
		// A doubled backslash is a TeX newline followed by literal "ce".
		if index > 0 && formula[index-1] == '\\' {
			out.WriteString(formula[at : index+3])
			at = index + 3
			continue
		}
		if index+3 < len(formula) && unicode.IsLetter(rune(formula[index+3])) {
			out.WriteString(formula[at : index+3])
			at = index + 3
			continue
		}
		out.WriteString(formula[at:index])
		if index+3 >= len(formula) || formula[index+3] != '{' {
			out.WriteString(chemistryLiteral(formula[index : index+3]))
			at = index + 3
			continue
		}
		body, end, ok := chemistryBraceBody(formula, index+3)
		if !ok {
			out.WriteString(chemistryLiteral(formula[index:]))
			break
		}
		converted, parsed := parseChemistryBody(body)
		if parsed {
			out.WriteString(converted)
		} else {
			out.WriteString(chemistryLiteral(formula[index:end]))
		}
		at = end
	}
	return out.String()
}

func chemistryBraceBody(value string, opening int) (string, int, bool) {
	if opening >= len(value) || value[opening] != '{' {
		return "", 0, false
	}
	depth := 0
	for i := opening; i < len(value); i++ {
		if value[i] == '\\' && i+1 < len(value) && (value[i+1] == '{' || value[i+1] == '}') {
			i++
			continue
		}
		switch value[i] {
		case '{':
			depth++
			if depth > maxChemistryBraceDepth {
				return "", 0, false
			}
		case '}':
			depth--
			if depth == 0 {
				return value[opening+1 : i], i + 1, true
			}
		}
	}
	return "", 0, false
}

func chemistryLiteral(value string) string {
	var out strings.Builder
	out.WriteString(`\text{`)
	for _, r := range value {
		switch r {
		case '\\':
			out.WriteRune('＼')
		case '{':
			out.WriteString(`\{`)
		case '}':
			out.WriteString(`\}`)
		case '_':
			out.WriteString(`\_`)
		case '^':
			out.WriteRune('＾')
		case '&':
			out.WriteString(`\&`)
		case '%':
			out.WriteString(`\%`)
		case '#':
			out.WriteString(`\#`)
		case '$':
			out.WriteString(`\$`)
		case '~':
			out.WriteRune('～')
		default:
			out.WriteRune(r)
		}
	}
	out.WriteByte('}')
	return out.String()
}

type chemistryParser struct {
	s     string
	i     int
	depth int
}

func parseChemistryBody(body string) (string, bool) {
	if body == "" || len(body) > maxChemistryDisplayBytes {
		return "", false
	}
	p := chemistryParser{s: body}
	p.skipSpace()
	first, ok := p.species()
	if !ok {
		return "", false
	}
	var out strings.Builder
	out.WriteString(first)
	for {
		p.skipSpace()
		if p.i == len(p.s) {
			return out.String(), true
		}
		if p.s[p.i] == '^' && (p.i+1 == len(p.s) || p.s[p.i+1] == ' ' || p.s[p.i+1] == '+') {
			out.WriteString(`\uparrow`)
			p.i++
			p.skipSpace()
		}
		if p.i < len(p.s) && p.s[p.i] == 'v' && (p.i+1 == len(p.s) || p.s[p.i+1] == ' ' || p.s[p.i+1] == '+') {
			out.WriteString(`\downarrow`)
			p.i++
			p.skipSpace()
		}
		if p.i == len(p.s) {
			return out.String(), true
		}
		separator, ok := p.separator()
		if !ok {
			return "", false
		}
		out.WriteString(separator)
		p.skipSpace()
		next, ok := p.species()
		if !ok {
			return "", false
		}
		out.WriteString(next)
	}
}

func (p *chemistryParser) skipSpace() {
	for p.i < len(p.s) && (p.s[p.i] == ' ' || p.s[p.i] == '\t' || p.s[p.i] == '\n') {
		p.i++
	}
}

func (p *chemistryParser) separator() (string, bool) {
	arrows := []struct{ source, latex string }{
		{"<=>", ` \rightleftharpoons `},
		{"<->", ` \leftrightarrow `},
		{"->", ` \rightarrow `},
		{"<-", ` \leftarrow `},
	}
	for _, arrow := range arrows {
		if strings.HasPrefix(p.s[p.i:], arrow.source) {
			p.i += len(arrow.source)
			if strings.HasPrefix(p.s[p.i:], "[") {
				if arrow.source != "->" {
					return "", false
				}
				end := strings.IndexByte(p.s[p.i+1:], ']')
				if end < 0 {
					return "", false
				}
				condition := p.s[p.i+1 : p.i+1+end]
				if condition != `\Delta` && condition != "Δ" {
					return "", false
				}
				p.i += end + 2
				return ` \xrightarrow{\Delta} `, true
			}
			return arrow.latex, true
		}
	}
	if p.s[p.i] == '=' {
		p.i++
		return " = ", true
	}
	if p.s[p.i] == '+' {
		p.i++
		return " + ", true
	}
	return "", false
}

func (p *chemistryParser) species() (string, bool) {
	start := p.i
	coefficient := p.digits()
	if coefficient != "" {
		p.skipSpace()
	}
	core, atoms, ok := p.molecule()
	if !ok {
		p.i = start
		return "", false
	}
	var out strings.Builder
	out.WriteString(coefficient)
	out.WriteString(core)
	for p.i < len(p.s) && (p.s[p.i] == '.' || strings.HasPrefix(p.s[p.i:], "·")) {
		if p.s[p.i] == '.' {
			p.i++
		} else {
			p.i += len("·")
		}
		out.WriteString(`\cdot`)
		out.WriteString(p.digits())
		part, count, valid := p.molecule()
		if !valid {
			return "", false
		}
		atoms += count
		out.WriteString(part)
	}
	if p.i < len(p.s) && p.s[p.i] == '^' && p.i+1 < len(p.s) && p.s[p.i+1] != ' ' {
		charge, valid := p.explicitCharge()
		if !valid {
			return "", false
		}
		out.WriteString(charge)
	} else if p.i < len(p.s) && (p.s[p.i] == '+' || p.s[p.i] == '-') {
		// A bare sign is a charge only when it closes the species. With a
		// following atom (H+O) its meaning is ambiguous, so reject it.
		if p.i+1 < len(p.s) && isChemistryAtomStart(p.s[p.i+1]) {
			return "", false
		}
		if atoms == 1 && strings.Contains(core, "}_{") {
			// Ca2+ may mean Ca_2^+ or Ca^{2+}; do not guess.
			return "", false
		}
		out.WriteString(`^{` + p.s[p.i:p.i+1] + `}`)
		p.i++
	}
	return out.String(), true
}

func (p *chemistryParser) molecule() (string, int, bool) {
	var out strings.Builder
	atoms := 0
	for p.i < len(p.s) {
		if p.s[p.i] == '(' {
			group, count, ok := p.group()
			if !ok {
				return "", 0, false
			}
			out.WriteString(group)
			atoms += count
			continue
		}
		if !isChemistryAtomStart(p.s[p.i]) {
			break
		}
		atom, ok := p.atom()
		if !ok {
			return "", 0, false
		}
		out.WriteString(atom)
		atoms++
	}
	return out.String(), atoms, atoms > 0
}

func (p *chemistryParser) group() (string, int, bool) {
	if p.depth >= maxChemistryGroupDepth || p.s[p.i] != '(' {
		return "", 0, false
	}
	p.depth++
	p.i++
	inside, atoms, ok := p.molecule()
	if !ok || p.i >= len(p.s) || p.s[p.i] != ')' {
		return "", 0, false
	}
	p.i++
	p.depth--
	count := p.digits()
	if count != "" {
		return "(" + inside + ")_{" + count + "}", atoms, true
	}
	return "(" + inside + ")", atoms, true
}

func (p *chemistryParser) atom() (string, bool) {
	if p.s[p.i] == '\\' {
		if !strings.HasPrefix(p.s[p.i:], `\mathrm{`) {
			return "", false
		}
		body, end, ok := chemistryBraceBody(p.s, p.i+len(`\mathrm`))
		if !ok || strings.Contains(body, `\`) {
			return "", false
		}
		converted, valid := parseChemistryBody(body)
		if !valid {
			return "", false
		}
		p.i = end
		return converted, true
	}
	if p.s[p.i] == 'e' {
		p.i++
		return `\mathrm{e}`, true
	}
	start := p.i
	p.i++
	if p.i < len(p.s) && p.s[p.i] >= 'a' && p.s[p.i] <= 'z' {
		p.i++
	}
	symbol := p.s[start:p.i]
	if !chemistryElements[symbol] {
		return "", false
	}
	count := p.digits()
	if count == "" {
		return `\mathrm{` + symbol + `}`, true
	}
	return `\mathrm{` + symbol + `}_{` + count + `}`, true
}

func (p *chemistryParser) digits() string {
	start := p.i
	for p.i < len(p.s) && p.s[p.i] >= '0' && p.s[p.i] <= '9' {
		p.i++
	}
	return p.s[start:p.i]
}

func (p *chemistryParser) explicitCharge() (string, bool) {
	if p.s[p.i] != '^' {
		return "", false
	}
	p.i++
	braced := p.i < len(p.s) && p.s[p.i] == '{'
	if braced {
		p.i++
	}
	magnitude := p.digits()
	if p.i >= len(p.s) || (p.s[p.i] != '+' && p.s[p.i] != '-') {
		return "", false
	}
	sign := p.s[p.i : p.i+1]
	p.i++
	if braced {
		if p.i >= len(p.s) || p.s[p.i] != '}' {
			return "", false
		}
		p.i++
	}
	return `^{` + magnitude + sign + `}`, true
}

func isChemistryAtomStart(c byte) bool {
	return c >= 'A' && c <= 'Z' || c == 'e' || c == '\\'
}
