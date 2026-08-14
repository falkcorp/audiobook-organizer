// file: internal/search/query_parser.go
// version: 1.2.0
// guid: 3c4a1d8f-5b2e-4f70-a7c6-2f8d0e1b9a57
// last-edited: 2026-08-13
//
// Parser for the library search DSL (spec 3.4 / DES-1 v1.1).
//
// Grammar (informal):
//   expr     := orExpr
//   orExpr   := andExpr ( ('||' | 'OR') andExpr )*
//   andExpr  := notExpr ( ('&&' | 'AND' | ε) notExpr )*
//   notExpr  := ('-' | 'NOT') notExpr | primary
//   primary  := '(' expr ')' | fieldOrToken
//   fieldOrToken
//            := field ':' (value | valueAlt)
//             | token
//   valueAlt := '(' value ( '|' value )* ')'
//   value    := quotedStr | operated
//   operated := ('>'|'<'|'>='|'<='|'='|'range')? rawToken ('~'|'*')? ('^' number)?
//
// Operator precedence (tightest to loosest): NOT → AND → OR.
// Grouping with ( … ) overrides precedence.
//
// Whitespace is AND. Backward compat: existing queries like
//   author:smith tag:scifi -tag:romance
// parse identically.

package search

import (
	"fmt"
	"strconv"
	"strings"
	"unicode"
)

// ParseQuery parses a DSL query string into an AST. Returns a
// non-nil error on syntax failures; error messages include the
// approximate cursor position.
func ParseQuery(input string) (Node, error) {
	p := &parser{input: input, pos: 0}
	node, err := p.parseOr()
	if err != nil {
		return nil, err
	}
	p.skipWhitespace()
	if p.pos < len(p.input) {
		return nil, fmt.Errorf("unexpected trailing input at position %d: %q", p.pos, p.input[p.pos:])
	}
	return node, nil
}

type parser struct {
	input string
	pos   int
}

func (p *parser) peek() byte {
	if p.pos >= len(p.input) {
		return 0
	}
	return p.input[p.pos]
}

func (p *parser) peekN(n int) string {
	end := p.pos + n
	if end > len(p.input) {
		end = len(p.input)
	}
	return p.input[p.pos:end]
}

func (p *parser) skipWhitespace() {
	for p.pos < len(p.input) && unicode.IsSpace(rune(p.input[p.pos])) {
		p.pos++
	}
}

// consumeKeyword matches one of the word-form operators (AND, OR,
// NOT) as a whole word. Returns true + advances past it if matched.
// Case-sensitive — the word forms are uppercase by convention.
func (p *parser) consumeKeyword(kw string) bool {
	if p.pos+len(kw) > len(p.input) {
		return false
	}
	if p.input[p.pos:p.pos+len(kw)] != kw {
		return false
	}
	// Require word boundary after the keyword (whitespace or end or
	// punctuation) so we don't accidentally match "ANDROID" for AND.
	if p.pos+len(kw) < len(p.input) {
		next := p.input[p.pos+len(kw)]
		if next != ' ' && next != '\t' && next != '(' && next != '-' {
			return false
		}
	}
	p.pos += len(kw)
	return true
}

func (p *parser) parseOr() (Node, error) {
	left, err := p.parseAnd()
	if err != nil {
		return nil, err
	}
	children := []Node{left}
	for {
		p.skipWhitespace()
		if p.peekN(2) == "||" {
			p.pos += 2
		} else if p.consumeKeyword("OR") {
			// matched
		} else {
			break
		}
		p.skipWhitespace()
		right, err := p.parseAnd()
		if err != nil {
			return nil, err
		}
		children = append(children, right)
	}
	if len(children) == 1 {
		return children[0], nil
	}
	return &OrNode{Children: children}, nil
}

func (p *parser) parseAnd() (Node, error) {
	left, err := p.parseNot()
	if err != nil {
		return nil, err
	}
	children := []Node{left}
	for {
		p.skipWhitespace()
		if p.pos >= len(p.input) {
			break
		}
		// Stop if we're at a closing paren or an OR operator.
		if p.peek() == ')' {
			break
		}
		if p.peekN(2) == "||" {
			break
		}
		if p.pos+2 <= len(p.input) && p.input[p.pos:p.pos+2] == "OR" {
			// Only treat as OR if followed by whitespace / EOL / paren.
			if p.pos+2 == len(p.input) || p.input[p.pos+2] == ' ' || p.input[p.pos+2] == '\t' || p.input[p.pos+2] == '(' {
				break
			}
		}
		// Consume an explicit && or AND if present; else fall through
		// (implicit-whitespace AND).
		if p.peekN(2) == "&&" {
			p.pos += 2
			p.skipWhitespace()
		} else if p.consumeKeyword("AND") {
			p.skipWhitespace()
		}
		right, err := p.parseNot()
		if err != nil {
			return nil, err
		}
		children = append(children, right)
	}
	if len(children) == 1 {
		return children[0], nil
	}
	return &AndNode{Children: children}, nil
}

func (p *parser) parseNot() (Node, error) {
	p.skipWhitespace()
	// '-' prefix form
	if p.peek() == '-' {
		// Only treat as NOT if followed by a non-space token (not a
		// date / year that happens to be negative).
		if p.pos+1 < len(p.input) && p.input[p.pos+1] != ' ' {
			p.pos++
			inner, err := p.parseNot()
			if err != nil {
				return nil, err
			}
			return &NotNode{Child: inner}, nil
		}
	}
	// 'NOT' keyword
	if p.consumeKeyword("NOT") {
		p.skipWhitespace()
		inner, err := p.parseNot()
		if err != nil {
			return nil, err
		}
		return &NotNode{Child: inner}, nil
	}
	return p.parsePrimary()
}

func (p *parser) parsePrimary() (Node, error) {
	p.skipWhitespace()
	if p.pos >= len(p.input) {
		return nil, fmt.Errorf("unexpected end of input at position %d", p.pos)
	}
	if p.peek() == '(' {
		p.pos++
		inner, err := p.parseOr()
		if err != nil {
			return nil, err
		}
		p.skipWhitespace()
		if p.peek() != ')' {
			return nil, fmt.Errorf("expected ')' at position %d", p.pos)
		}
		p.pos++
		return inner, nil
	}
	return p.parseFieldOrToken()
}

// parseFieldOrToken reads a "field:value" clause, or a bare token
// as free text. Handles quoting, value-alternation, and the
// suffix operators * / ~ / ^boost.
func (p *parser) parseFieldOrToken() (Node, error) {
	// A bare quoted phrase: "all jobs".
	//
	// This must be handled BEFORE the bare-token scan below, which stops at
	// whitespace and would therefore split `"All Jobs"` into two tokens,
	// `"All` and `Jobs"`, with the quote characters still attached. The
	// English analyser then discards those quotes as punctuation, so the
	// query behaved exactly like the unquoted one and FreeTextNode.Quoted
	// was never set — leaving the MatchPhraseQuery branch in the translator
	// unreachable for bare free text. Measured on production 2026-08-13:
	// `"All Jobs"` returned 300 rows, byte-identical to unquoted `All Jobs`,
	// topped by "Side Jobs" and "The Icarus Job".
	//
	// The field-scoped form (title:"all jobs") was never affected — it goes
	// through parseFieldValue, which has always handled quotes.
	if p.peek() == '"' {
		phrase, err := p.parseQuoted()
		if err != nil {
			return nil, err
		}
		// Swallow a '*' glued to the closing quote. The web UI appends one
		// to whatever was typed, and letting it fall through would leave a
		// lone '*' for the next iteration to parse as an empty-prefix token,
		// which matches every document. A prefix-of-a-phrase is not a query
		// Bleve offers, so the phrase itself is the honest interpretation.
		if p.peek() == '*' {
			p.pos++
		}
		return &FreeTextNode{Value: phrase, Quoted: true}, nil
	}

	// Read the identifier up to : or whitespace / special char.
	start := p.pos
	for p.pos < len(p.input) {
		c := p.peek()
		if c == ':' || c == ' ' || c == '\t' || c == ')' || c == '(' {
			break
		}
		if p.peekN(2) == "||" || p.peekN(2) == "&&" {
			break
		}
		p.pos++
	}
	head := p.input[start:p.pos]
	if head == "" {
		return nil, fmt.Errorf("expected token at position %d", p.pos)
	}

	if p.peek() != ':' {
		// Bare token → FreeTextNode.
		return parseFreeText(head), nil
	}

	// Consume ':' and parse the value.
	p.pos++
	return p.parseFieldValue(head)
}

func parseFreeText(tok string) Node {
	n := &FreeTextNode{Value: tok}
	// Handle trailing * and ~ on free text too.
	if strings.HasSuffix(n.Value, "~") {
		n.Fuzzy = true
		n.Value = strings.TrimSuffix(n.Value, "~")
	}
	if strings.HasSuffix(n.Value, "*") {
		n.Prefix = true
		n.Value = strings.TrimSuffix(n.Value, "*")
	}
	return n
}

func (p *parser) parseFieldValue(field string) (Node, error) {
	p.skipWhitespace()
	if p.pos >= len(p.input) {
		return nil, fmt.Errorf("expected value after '%s:' at position %d", field, p.pos)
	}

	// Value-alternation: field:(a|b|c)
	if p.peek() == '(' {
		return p.parseValueAlt(field)
	}

	// Numeric range: field:[A TO B]
	if p.peek() == '[' {
		return p.parseRange(field)
	}

	// Numeric comparator prefix: >, <, >=, <=, =
	op := ""
	if p.pos+2 <= len(p.input) && (p.input[p.pos:p.pos+2] == ">=" || p.input[p.pos:p.pos+2] == "<=") {
		op = p.input[p.pos : p.pos+2]
		p.pos += 2
	} else if c := p.peek(); c == '>' || c == '<' || c == '=' {
		op = string(c)
		p.pos++
	}

	// Quoted or bare value.
	var value string
	var quoted bool
	if p.peek() == '"' {
		var err error
		value, err = p.parseQuoted()
		if err != nil {
			return nil, err
		}
		quoted = true
	} else {
		value = p.parseBareValue()
	}

	node := &FieldNode{
		Field: field, Value: value, Quoted: quoted, Op: op,
	}

	// Trailing ~, *, ^N — applied to whatever value we parsed.
	if strings.HasSuffix(value, "~") && !quoted {
		node.Fuzzy = true
		node.Value = strings.TrimSuffix(value, "~")
	}
	value = node.Value
	if strings.HasSuffix(value, "*") && !quoted {
		node.Prefix = true
		node.Value = strings.TrimSuffix(value, "*")
	}
	value = node.Value
	if strings.HasPrefix(value, "*") && !quoted {
		node.Wildcard = true
		node.Value = strings.TrimPrefix(value, "*")
	}
	// Boost: field:foo^3
	if idx := strings.LastIndex(node.Value, "^"); idx > 0 && !quoted {
		boostStr := node.Value[idx+1:]
		if f, err := strconv.ParseFloat(boostStr, 64); err == nil {
			node.Boost = f
			node.Value = node.Value[:idx]
		}
	}
	return node, nil
}

func (p *parser) parseQuoted() (string, error) {
	if p.peek() != '"' {
		return "", fmt.Errorf("expected opening quote at position %d", p.pos)
	}
	p.pos++
	start := p.pos
	for p.pos < len(p.input) && p.peek() != '"' {
		p.pos++
	}
	if p.pos >= len(p.input) {
		return "", fmt.Errorf("unterminated quoted value starting at position %d", start)
	}
	val := p.input[start:p.pos]
	p.pos++ // consume closing quote
	return val, nil
}

// parseBareValue reads an unquoted value up to the next operator
// or whitespace. Supports `|` only inside value-alternation parens.
func (p *parser) parseBareValue() string {
	start := p.pos
	for p.pos < len(p.input) {
		c := p.peek()
		if c == ' ' || c == '\t' || c == ')' {
			break
		}
		if p.peekN(2) == "||" || p.peekN(2) == "&&" {
			break
		}
		p.pos++
	}
	return p.input[start:p.pos]
}

func (p *parser) parseValueAlt(field string) (Node, error) {
	if p.peek() != '(' {
		return nil, fmt.Errorf("expected '(' at position %d", p.pos)
	}
	p.pos++ // consume '('
	var values []string
	for {
		p.skipWhitespace()
		var v string
		if p.peek() == '"' {
			qv, err := p.parseQuoted()
			if err != nil {
				return nil, err
			}
			v = qv
		} else {
			start := p.pos
			for p.pos < len(p.input) && p.peek() != '|' && p.peek() != ')' {
				p.pos++
			}
			v = strings.TrimSpace(p.input[start:p.pos])
		}
		if v != "" {
			values = append(values, v)
		}
		if p.peek() == '|' {
			p.pos++
			continue
		}
		break
	}
	p.skipWhitespace()
	if p.peek() != ')' {
		return nil, fmt.Errorf("expected ')' closing value-alternation at position %d", p.pos)
	}
	p.pos++
	return &ValueAltNode{Field: field, Values: values}, nil
}

func (p *parser) parseRange(field string) (Node, error) {
	if p.peek() != '[' {
		return nil, fmt.Errorf("expected '[' at position %d", p.pos)
	}
	p.pos++ // consume '['
	// Read up to ']'. Expect "A TO B" (case-insensitive TO).
	start := p.pos
	for p.pos < len(p.input) && p.peek() != ']' {
		p.pos++
	}
	if p.peek() != ']' {
		return nil, fmt.Errorf("unterminated range starting at position %d", start)
	}
	body := p.input[start:p.pos]
	p.pos++ // consume ']'

	// Split on " TO " (case-insensitive).
	lower := strings.ToLower(body)
	idx := strings.Index(lower, " to ")
	if idx < 0 {
		return nil, fmt.Errorf("range must be of form [A TO B], got %q", body)
	}
	minVal := strings.TrimSpace(body[:idx])
	maxVal := strings.TrimSpace(body[idx+4:])
	return &FieldNode{
		Field: field, Op: "range",
		RangeMin: minVal, RangeMax: maxVal,
	}, nil
}

// QuoteIfNeeded wraps a string in quotes if it contains spaces.
// Used by query builders to ensure multi-word values are treated as single tokens.
func QuoteIfNeeded(s string) string {
	if strings.Contains(s, " ") {
		return `"` + s + `"`
	}
	return s
}
