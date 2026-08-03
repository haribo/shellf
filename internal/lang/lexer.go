// Package lang parses shellf inventory and plan files into the runtime types.
// Whitespace and newlines are insignificant: every construct is bracket- or
// paren-delimited, so no statement separators are needed. Comments start with #.
package lang

import (
	"fmt"
	"strings"
)

type tokKind int

const (
	tEOF tokKind = iota
	tIdent
	tString
	tNumber
	tLBrace
	tRBrace
	tLBrack
	tRBrack
	tLParen
	tRParen
	tEq
	tColon
	tComma
	tDot
	tEqEq      // ==
	tNotEq     // !=
	tArrow     // ->
	tBang      // !
	tQuestion  // ? — marks a failible instruction as caught (ADR-0009)
	tRawString // """ … """ — raw, never interpolated
)

type token struct {
	kind tokKind
	val  string
	line int
	col  int
}

type lexer struct {
	src      string
	pos      int
	line     int
	col      int
	tokStart int // byte offset where the last-returned token began (for source spans)
}

func newLexer(src string) *lexer { return &lexer{src: src, line: 1, col: 1} }

func (l *lexer) next() (token, error) {
	l.skip()
	l.tokStart = l.pos
	if l.pos >= len(l.src) {
		return token{kind: tEOF, line: l.line, col: l.col}, nil
	}
	line, col := l.line, l.col
	c := l.src[l.pos]

	// Two-char operators (before the single-char map, which owns '=').
	switch {
	case c == '=' && l.peek() == '=':
		l.adv()
		l.adv()
		return token{tEqEq, "==", line, col}, nil
	case c == '!' && l.peek() == '=':
		l.adv()
		l.adv()
		return token{tNotEq, "!=", line, col}, nil
	case c == '-' && l.peek() == '>':
		l.adv()
		l.adv()
		return token{tArrow, "->", line, col}, nil
	}

	if punct, ok := punctuation[c]; ok {
		l.adv()
		return token{punct, string(c), line, col}, nil
	}
	if c == '"' {
		if l.pos+2 < len(l.src) && l.src[l.pos+1] == '"' && l.src[l.pos+2] == '"' {
			return l.lexTripleString(line, col)
		}
		return l.lexString(line, col)
	}
	if c >= '0' && c <= '9' {
		return l.lexNumber(line, col), nil
	}
	if isIdentStart(c) {
		return l.lexIdent(line, col), nil
	}
	return token{}, fmt.Errorf("%d:%d: unexpected character %q", line, col, string(c))
}

var punctuation = map[byte]tokKind{
	'{': tLBrace, '}': tRBrace, '[': tLBrack, ']': tRBrack,
	'(': tLParen, ')': tRParen, '=': tEq, ':': tColon, ',': tComma, '.': tDot,
	'!': tBang, // '!=' is handled before this map, so a lone '!' lands here
	'?': tQuestion,
}

func (l *lexer) skip() {
	for l.pos < len(l.src) {
		switch l.src[l.pos] {
		case ' ', '\t', '\r', '\n':
			l.adv()
		case '#':
			for l.pos < len(l.src) && l.src[l.pos] != '\n' {
				l.adv()
			}
		default:
			return
		}
	}
}

func (l *lexer) peek() byte {
	if l.pos+1 < len(l.src) {
		return l.src[l.pos+1]
	}
	return 0
}

func (l *lexer) adv() {
	if l.src[l.pos] == '\n' {
		l.line++
		l.col = 1
	} else {
		l.col++
	}
	l.pos++
}

func (l *lexer) lexString(line, col int) (token, error) {
	l.adv() // opening quote
	var b strings.Builder
	for l.pos < len(l.src) {
		c := l.src[l.pos]
		switch c {
		case '"':
			l.adv()
			return token{tString, b.String(), line, col}, nil
		case '\\':
			l.adv()
			if l.pos >= len(l.src) {
				return token{}, fmt.Errorf("%d:%d: unterminated string", line, col)
			}
			b.WriteByte(unescape(l.src[l.pos]))
			l.adv()
		default:
			b.WriteByte(c)
			l.adv()
		}
	}
	return token{}, fmt.Errorf("%d:%d: unterminated string", line, col)
}

// lexTripleString reads a raw triple-quoted string: everything between """ and
// the next """, verbatim (newlines preserved, no escape processing).
func (l *lexer) lexTripleString(line, col int) (token, error) {
	l.adv()
	l.adv()
	l.adv() // opening """
	rest := l.src[l.pos:]
	end := strings.Index(rest, `"""`)
	if end < 0 {
		return token{}, fmt.Errorf("%d:%d: unterminated triple-quoted string", line, col)
	}
	s := rest[:end]
	for i := 0; i < end+3; i++ {
		l.adv() // content + closing """
	}
	return token{tRawString, dedentTriple(s), line, col}, nil
}

// dedentTriple strips the block's indentation from a multi-line triple-quoted
// string, so the content can be indented for readability in the plan. The
// margin is the whitespace preceding the closing """ (on its own line); it is
// removed as a prefix from every line, and the newline delimiters right after
// the opening """ and right before the closing """ are dropped. Swift/Kotlin
// semantics. A single-line string, or content sharing the closing """'s line,
// is returned unchanged — so flush-left blocks (margin 0) stay as they were.
func dedentTriple(s string) string {
	nl := strings.LastIndexByte(s, '\n')
	if nl < 0 {
		return s // single line: nothing to dedent
	}
	margin := s[nl+1:]
	if strings.TrimLeft(margin, " \t") != "" {
		return s // content on the closing line: not a block form
	}
	body := strings.TrimPrefix(s[:nl], "\n") // drop the trailing margin line and a leading newline
	if margin == "" {
		return body
	}
	lines := strings.Split(body, "\n")
	for i, ln := range lines {
		lines[i] = strings.TrimPrefix(ln, margin)
	}
	return strings.Join(lines, "\n")
}

func unescape(c byte) byte {
	switch c {
	case 'n':
		return '\n'
	case 't':
		return '\t'
	default:
		return c // ", \, and anything else literal
	}
}

func (l *lexer) lexNumber(line, col int) token {
	start := l.pos
	for l.pos < len(l.src) && l.src[l.pos] >= '0' && l.src[l.pos] <= '9' {
		l.adv()
	}
	return token{tNumber, l.src[start:l.pos], line, col}
}

func (l *lexer) lexIdent(line, col int) token {
	start := l.pos
	for l.pos < len(l.src) && isIdentPart(l.src[l.pos]) {
		l.adv()
	}
	return token{tIdent, l.src[start:l.pos], line, col}
}

// --- raw capture, for the `shell` special form ---
//
// These read source verbatim (no tokenizing) from the current lexer position,
// which the parser calls right after consuming the `shell` / `unless` keyword.

// skipInline skips spaces and tabs but not newlines.
func (l *lexer) skipInline() {
	for l.pos < len(l.src) && (l.src[l.pos] == ' ' || l.src[l.pos] == '\t') {
		l.adv()
	}
}

// rawShellBody reads a `{ … }` block (balanced braces) if the next non-space
// char is `{`, otherwise the rest of the current line.
func (l *lexer) rawShellBody() (string, error) {
	l.skipInline()
	if l.pos < len(l.src) && l.src[l.pos] == '{' {
		return l.rawBraces()
	}
	return l.rawLine(), nil
}

// rawBracesRequired reads a `{ … }` block; used for the `unless` guard.
func (l *lexer) rawBracesRequired() (string, error) {
	l.skipInline()
	if l.pos >= len(l.src) || l.src[l.pos] != '{' {
		return "", fmt.Errorf("%d:%d: expected '{' after unless", l.line, l.col)
	}
	return l.rawBraces()
}

func (l *lexer) rawLine() string {
	start := l.pos
	for l.pos < len(l.src) && l.src[l.pos] != '\n' {
		l.adv()
	}
	return strings.TrimSpace(l.src[start:l.pos])
}

// rawBraces captures up to the balanced closing brace. Braces inside shell
// strings are NOT understood — a lone unbalanced brace ends the block early.
func (l *lexer) rawBraces() (string, error) {
	l.adv() // consume '{'
	start := l.pos
	depth := 1
	for l.pos < len(l.src) {
		switch l.src[l.pos] {
		case '{':
			depth++
		case '}':
			depth--
			if depth == 0 {
				body := l.src[start:l.pos]
				l.adv() // consume '}'
				return strings.TrimSpace(body), nil
			}
		}
		l.adv()
	}
	return "", fmt.Errorf("unterminated shell block")
}

// tryRawWord consumes the whole word w from the raw stream (after inline
// spaces), used for the `shell as <user>` prefix. Returns false without
// advancing if w is not next.
func (l *lexer) tryRawWord(w string) bool {
	save, sl, sc := l.pos, l.line, l.col
	l.skipInline()
	if !strings.HasPrefix(l.src[l.pos:], w) {
		l.pos, l.line, l.col = save, sl, sc
		return false
	}
	if end := l.pos + len(w); end < len(l.src) && isIdentPart(l.src[end]) {
		l.pos, l.line, l.col = save, sl, sc // not a whole word (e.g. "assign")
		return false
	}
	for i := 0; i < len(w); i++ {
		l.adv()
	}
	return true
}

// tryRawByte consumes the byte c from the raw stream (after inline spaces) if
// present, e.g. the `(` / `)` around `shell(bash)`.
func (l *lexer) tryRawByte(c byte) bool {
	l.skipInline()
	if l.pos < len(l.src) && l.src[l.pos] == c {
		l.adv()
		return true
	}
	return false
}

// rawIdent reads an identifier from the raw stream (after inline spaces).
func (l *lexer) rawIdent() string {
	l.skipInline()
	start := l.pos
	for l.pos < len(l.src) && isIdentPart(l.src[l.pos]) {
		l.adv()
	}
	return l.src[start:l.pos]
}

func isIdentStart(c byte) bool {
	return c == '_' || (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z')
}

func isIdentPart(c byte) bool {
	return isIdentStart(c) || (c >= '0' && c <= '9') || c == '-'
}
