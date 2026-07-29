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
	tLBrace
	tRBrace
	tLBrack
	tRBrack
	tLParen
	tRParen
	tEq
	tColon
	tComma
)

type token struct {
	kind tokKind
	val  string
	line int
	col  int
}

type lexer struct {
	src  string
	pos  int
	line int
	col  int
}

func newLexer(src string) *lexer { return &lexer{src: src, line: 1, col: 1} }

func (l *lexer) next() (token, error) {
	l.skip()
	if l.pos >= len(l.src) {
		return token{kind: tEOF, line: l.line, col: l.col}, nil
	}
	line, col := l.line, l.col
	c := l.src[l.pos]

	if punct, ok := punctuation[c]; ok {
		l.adv()
		return token{punct, string(c), line, col}, nil
	}
	if c == '"' {
		return l.lexString(line, col)
	}
	if isIdentStart(c) {
		return l.lexIdent(line, col), nil
	}
	return token{}, fmt.Errorf("%d:%d: unexpected character %q", line, col, string(c))
}

var punctuation = map[byte]tokKind{
	'{': tLBrace, '}': tRBrace, '[': tLBrack, ']': tRBrack,
	'(': tLParen, ')': tRParen, '=': tEq, ':': tColon, ',': tComma,
}

func (l *lexer) skip() {
	for l.pos < len(l.src) {
		switch c := l.src[l.pos]; {
		case c == ' ' || c == '\t' || c == '\r' || c == '\n':
			l.adv()
		case c == '#':
			for l.pos < len(l.src) && l.src[l.pos] != '\n' {
				l.adv()
			}
		default:
			return
		}
	}
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

func (l *lexer) lexIdent(line, col int) token {
	start := l.pos
	for l.pos < len(l.src) && isIdentPart(l.src[l.pos]) {
		l.adv()
	}
	return token{tIdent, l.src[start:l.pos], line, col}
}

func isIdentStart(c byte) bool {
	return c == '_' || (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z')
}

func isIdentPart(c byte) bool {
	return isIdentStart(c) || (c >= '0' && c <= '9') || c == '-'
}
