package bridge

import (
	"strings"
	"unicode/utf8"
)

// scanDisplayAdapter converts complete model math spans into the existing
// YDRichText latex tag. Raw and display output are committed together, so an
// unfinished formula cannot leak into the native display fallback.
const maxPendingScanFormula = 8 * 1024

type scanDisplayMode uint8

const (
	scanPlain scanDisplayMode = iota
	scanInlineCode
	scanCodeFence
	scanMath
	scanPassthrough
)

type scanDisplayAdapter struct {
	pending    string
	mode       scanDisplayMode
	lineStart  bool
	lineIndent int
	codeTicks  int
	mathOpen   string
	mathClose  string
}

func newScanDisplayAdapter() *scanDisplayAdapter {
	return &scanDisplayAdapter{lineStart: true}
}

func (a *scanDisplayAdapter) consume(delta string) (raw, display string) {
	a.pending += delta
	var rawOut, displayOut strings.Builder
	commit := func(source, shown string) {
		rawOut.WriteString(source)
		displayOut.WriteString(shown)
		for i := 0; i < len(source); i++ {
			switch {
			case source[i] == '\n':
				a.lineStart = true
				a.lineIndent = 0
			case a.lineStart && source[i] == ' ' && a.lineIndent < 3:
				a.lineIndent++
			default:
				a.lineStart = false
			}
		}
	}
	for len(a.pending) > 0 {
		if a.mode == scanMath {
			end := mathCloseIndex(a.pending, a.mathOpen, a.mathClose)
			if end >= 0 {
				span := a.pending[:end+len(a.mathClose)]
				inside := span[len(a.mathOpen) : len(span)-len(a.mathClose)]
				shown := span
				if len(span) <= maxPendingScanFormula && strings.TrimSpace(inside) != "" && utf8.ValidString(inside) {
					shown = latexTag(inside)
				}
				commit(span, shown)
				a.pending = a.pending[len(span):]
				a.mode = scanPlain
				continue
			}
			if len(a.pending) > maxPendingScanFormula {
				// A malformed or very large formula is passed through unchanged.
				a.mode = scanPassthrough
				continue
			}
			break
		}
		if a.mode == scanPassthrough {
			n := validRunePrefix(a.pending)
			if n == 0 {
				break
			}
			commit(a.pending[:n], a.pending[:n])
			a.pending = a.pending[n:]
			continue
		}
		if a.mode == scanCodeFence {
			if a.lineStart && a.pending[0] == '`' {
				ticks := leadingTicks(a.pending)
				if ticks == len(a.pending) && ticks <= a.codeTicks {
					break
				}
				if ticks == a.codeTicks {
					a.mode = scanPlain
				}
				commit(a.pending[:ticks], a.pending[:ticks])
				a.pending = a.pending[ticks:]
				continue
			}
			n := firstRuneLen(a.pending)
			if n == 0 {
				break
			}
			commit(a.pending[:n], a.pending[:n])
			a.pending = a.pending[n:]
			continue
		}
		if a.pending[0] == '`' {
			ticks := leadingTicks(a.pending)
			if ticks == len(a.pending) && ticks < 16 {
				break
			}
			if a.mode == scanInlineCode {
				if ticks == a.codeTicks {
					a.mode = scanPlain
				}
			} else if ticks <= 16 {
				if a.lineStart && ticks >= 3 {
					a.mode = scanCodeFence
					a.codeTicks = ticks
				} else {
					a.mode = scanInlineCode
					a.codeTicks = ticks
				}
			}
			commit(a.pending[:ticks], a.pending[:ticks])
			a.pending = a.pending[ticks:]
			continue
		}
		if a.mode == scanInlineCode {
			n := firstRuneLen(a.pending)
			if n == 0 {
				break
			}
			commit(a.pending[:n], a.pending[:n])
			a.pending = a.pending[n:]
			continue
		}
		if a.pending[0] == '\\' {
			if len(a.pending) == 1 {
				break
			}
			if a.pending[1] == '[' || a.pending[1] == '(' {
				a.mode = scanMath
				a.mathOpen = a.pending[:2]
				if a.pending[1] == '[' {
					a.mathClose = "\\]"
				} else {
					a.mathClose = "\\)"
				}
				continue
			}
			if a.pending[1] == '$' || a.pending[1] == '`' || a.pending[1] == '\\' {
				commit(a.pending[:2], a.pending[:2])
				a.pending = a.pending[2:]
				continue
			}
		}
		if a.pending[0] == '$' {
			if len(a.pending) == 1 {
				break
			}
			a.mode = scanMath
			if a.pending[1] == '$' {
				a.mathOpen, a.mathClose = "$$", "$$"
			} else {
				a.mathOpen, a.mathClose = "$", "$"
			}
			continue
		}
		n := firstRuneLen(a.pending)
		if n == 0 {
			break
		}
		commit(a.pending[:n], a.pending[:n])
		a.pending = a.pending[n:]
	}
	return rawOut.String(), displayOut.String()
}

func (a *scanDisplayAdapter) finish() (raw, display string) {
	// Unclosed math and partial UTF-8 are emitted literally at the turn boundary.
	raw, display = a.pending, a.pending
	*a = scanDisplayAdapter{lineStart: true}
	return raw, display
}

func firstRuneLen(value string) int {
	if !utf8.FullRuneInString(value) {
		return 0
	}
	_, n := utf8.DecodeRuneInString(value)
	return n
}

func validRunePrefix(value string) int {
	for n := 0; n < len(value); {
		if !utf8.FullRuneInString(value[n:]) {
			return n
		}
		_, size := utf8.DecodeRuneInString(value[n:])
		n += size
	}
	return len(value)
}

func leadingTicks(value string) int {
	n := 0
	for n < len(value) && value[n] == '`' {
		n++
	}
	return n
}

func mathCloseIndex(value, open, close string) int {
	for at := len(open); at < len(value); at++ {
		if !strings.HasPrefix(value[at:], close) {
			continue
		}
		// A backslash-escaped dollar sign is formula content, not a closer.
		if close == "$" || close == "$$" {
			backslashes := 0
			for j := at - 1; j >= len(open) && value[j] == '\\'; j-- {
				backslashes++
			}
			if backslashes%2 == 1 {
				continue
			}
		}
		return at
	}
	return -1
}

func latexTag(formula string) string {
	formula = normalizeChemistryLatex(formula)
	var escaped strings.Builder
	for _, r := range formula {
		switch r {
		case '&':
			escaped.WriteString("&#38;")
		case '"':
			escaped.WriteString("&#34;")
		case '\'':
			escaped.WriteString("&#x27;")
		case '<':
			escaped.WriteString("&#60;")
		case '>':
			escaped.WriteString("&#62;")
		default:
			escaped.WriteRune(r)
		}
	}
	return `<latex style="color: white; line-height: 45px;font-size: 30px;" value="$` + escaped.String() + `$"/>`
}
