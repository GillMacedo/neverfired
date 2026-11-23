package main

import (
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"unicode"
	"unicode/utf8"
)

// This file holds a small PromQL parser. It keeps only what neverfired
// needs: selectors with their label matchers, and the shape of the
// expression (functions, aggregations, binary operators) so it can work out
// whether an expression can return anything when some selectors match no
// series. It accepts the PromQL that Prometheus 3 accepts for rules; an
// expression it can't parse is reported, never guessed.

type pqKind int

const (
	pqSelector pqKind = iota
	pqNumber
	pqString
	pqCall
	pqAgg
	pqBinary
	pqParen
	pqUnary
	pqSubquery
)

type pqMatcher struct {
	Name  string
	Op    string // =, !=, =~, !~
	Value string
	re    *regexp.Regexp
}

type pqNode struct {
	Kind pqKind
	// Selector
	Metric   string // metric name, "" if only matchers
	Matchers []pqMatcher
	Range    bool // a range selector, such as foo[5m]
	// Shifted is set when an offset or @ modifier applies to the
	// selector, so it may read samples from other times than the
	// evaluation time.
	Shifted bool
	Text    string
	// Call and Agg
	Func string
	Args []*pqNode
	// Binary
	Op          string
	LHS, RHS    *pqNode
	ReturnsBool bool
	Fill        bool // fill, fill_left or fill_right: missing sides get a value
	// Paren, Unary, Subquery
	Inner *pqNode
}

// matches reports whether a series with these labels (including __name__)
// is selected by the selector's matchers.
func (n *pqNode) matches(lbls map[string]string) bool {
	if n.Metric != "" && lbls["__name__"] != n.Metric {
		return false
	}
	for _, m := range n.Matchers {
		if !m.matches(lbls[m.Name]) {
			return false
		}
	}
	return true
}

func (m pqMatcher) matches(v string) bool {
	switch m.Op {
	case "=":
		return v == m.Value
	case "!=":
		return v != m.Value
	case "=~":
		return m.re.MatchString(v)
	case "!~":
		return !m.re.MatchString(v)
	}
	return false
}

// nameMatches reports whether a metric name could be selected, looking only
// at the metric name and any __name__ matchers.
func (n *pqNode) nameMatches(name string) bool {
	if n.Metric != "" && n.Metric != name {
		return false
	}
	for _, m := range n.Matchers {
		if m.Name == "__name__" && !m.matches(name) {
			return false
		}
	}
	return true
}

// selectors returns every selector in the expression, in order.
func (n *pqNode) selectors() []*pqNode {
	var out []*pqNode
	var walk func(*pqNode)
	walk = func(x *pqNode) {
		if x == nil {
			return
		}
		switch x.Kind {
		case pqSelector:
			out = append(out, x)
		case pqCall, pqAgg:
			for _, a := range x.Args {
				walk(a)
			}
		case pqBinary:
			walk(x.LHS)
			walk(x.RHS)
		case pqParen, pqUnary, pqSubquery:
			walk(x.Inner)
		}
	}
	walk(n)
	return out
}

func markShifted(n *pqNode) {
	for _, s := range n.selectors() {
		s.Shifted = true
	}
}

// ---- lexer ----

type pqTokKind int

const (
	tEOF pqTokKind = iota
	tIdent
	tNumber
	tString
	tDuration
	tOp
	tLParen
	tRParen
	tLBrace
	tRBrace
	tLBracket
	tRBracket
	tComma
	tColon
	tAt
)

type pqTok struct {
	kind pqTokKind
	val  string
	pos  int
}

type pqLexer struct {
	src  string
	toks []pqTok
}

// isIdentStart accepts Unicode letters too, as Prometheus does with the
// promql-unquoted-utf8-names feature.
func isIdentStart(r rune) bool {
	return r == '_' || r == ':' || (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r > 127 && unicode.IsLetter(r))
}

func isIdentChar(r rune) bool {
	return isIdentStart(r) || (r >= '0' && r <= '9')
}

var durationRe = regexp.MustCompile(`^(([0-9]+)(ms|s|m|h|d|w|y))+`)

func pqLex(src string) ([]pqTok, error) {
	var toks []pqTok
	i := 0
	brackets := 0
	for i < len(src) {
		r, size := utf8.DecodeRuneInString(src[i:])
		switch {
		case r == '#':
			// Comment to end of line.
			for i < len(src) && src[i] != '\n' {
				i++
			}
			continue
		case unicode.IsSpace(r):
			i += size
			continue
		case r == '(':
			toks = append(toks, pqTok{tLParen, "(", i})
		case r == ')':
			toks = append(toks, pqTok{tRParen, ")", i})
		case r == '{':
			toks = append(toks, pqTok{tLBrace, "{", i})
		case r == '}':
			toks = append(toks, pqTok{tRBrace, "}", i})
		case r == '[':
			brackets++
			toks = append(toks, pqTok{tLBracket, "[", i})
		case r == ']':
			brackets--
			toks = append(toks, pqTok{tRBracket, "]", i})
		case r == ':' && brackets > 0:
			// Inside brackets a colon separates a subquery's range and step.
			toks = append(toks, pqTok{tColon, ":", i})
		case r == ',':
			toks = append(toks, pqTok{tComma, ",", i})
		case r == '@':
			toks = append(toks, pqTok{tAt, "@", i})
		case r == '"' || r == '\'' || r == '`':
			s, n, err := lexString(src[i:])
			if err != nil {
				return nil, fmt.Errorf("at position %d: %v", i, err)
			}
			toks = append(toks, pqTok{tString, s, i})
			i += n
			continue
		case r >= '0' && r <= '9' || (r == '.' && i+1 < len(src) && src[i+1] >= '0' && src[i+1] <= '9'):
			// A duration such as 5m or 1h30m, else a number.
			if d := durationRe.FindString(src[i:]); d != "" {
				end := i + len(d)
				if end >= len(src) || src[end] == ':' || !isIdentChar(rune(src[end])) {
					toks = append(toks, pqTok{tDuration, d, i})
					i = end
					continue
				}
			}
			n := lexNumber(src[i:])
			if n == 0 {
				return nil, fmt.Errorf("at position %d: bad number", i)
			}
			if _, err := parseNumber(src[i : i+n]); err != nil {
				return nil, fmt.Errorf("at position %d: bad number %q", i, src[i:i+n])
			}
			toks = append(toks, pqTok{tNumber, src[i : i+n], i})
			i += n
			continue
		case isIdentStart(r):
			j := i
			for j < len(src) {
				r2, s2 := utf8.DecodeRuneInString(src[j:])
				if !isIdentChar(r2) {
					break
				}
				j += s2
			}
			word := src[i:j]
			toks = append(toks, pqTok{tIdent, word, i})
			i = j
			continue
		default:
			op := ""
			for _, cand := range []string{"==", "!=", ">=", "<=", "=~", "!~", "</", ">/", "+", "-", "*", "/", "%", "^", "=", ">", "<"} {
				if strings.HasPrefix(src[i:], cand) {
					op = cand
					break
				}
			}
			if op == "" {
				return nil, fmt.Errorf("at position %d: unexpected character %q", i, r)
			}
			toks = append(toks, pqTok{tOp, op, i})
			i += len(op)
			continue
		}
		i += size
	}
	toks = append(toks, pqTok{tEOF, "", len(src)})
	return toks, nil
}

func lexString(s string) (string, int, error) {
	q := s[0]
	if q == '`' {
		end := strings.IndexByte(s[1:], '`')
		if end < 0 {
			return "", 0, fmt.Errorf("unterminated raw string")
		}
		return s[1 : 1+end], end + 2, nil
	}
	i := 1
	for i < len(s) {
		switch s[i] {
		case '\\':
			i += 2
			continue
		case '\n':
			return "", 0, fmt.Errorf("unterminated string")
		case q:
			body := s[1:i]
			if q == '\'' {
				// strconv.Unquote wants double quotes; swap the quoting.
				body = strings.ReplaceAll(body, `\'`, `'`)
				body = strings.ReplaceAll(body, `"`, `\"`)
			}
			v, err := strconv.Unquote(`"` + body + `"`)
			if err != nil {
				return "", 0, fmt.Errorf("bad string %s", s[:i+1])
			}
			return v, i + 1, nil
		}
		i++
	}
	return "", 0, fmt.Errorf("unterminated string")
}

// lexNumber returns the length of the number at the start of s: decimal,
// float with exponent, or hexadecimal.
func lexNumber(s string) int {
	if strings.HasPrefix(s, "0x") || strings.HasPrefix(s, "0X") {
		i := 2
		for i < len(s) && strings.ContainsRune("0123456789abcdefABCDEF_", rune(s[i])) {
			i++
		}
		return i
	}
	i := 0
	for i < len(s) && (s[i] >= '0' && s[i] <= '9' || s[i] == '_') {
		i++
	}
	if i < len(s) && s[i] == '.' {
		i++
		for i < len(s) && (s[i] >= '0' && s[i] <= '9' || s[i] == '_') {
			i++
		}
	}
	if i < len(s) && (s[i] == 'e' || s[i] == 'E') {
		j := i + 1
		if j < len(s) && (s[j] == '+' || s[j] == '-') {
			j++
		}
		if j < len(s) && s[j] >= '0' && s[j] <= '9' {
			for j < len(s) && s[j] >= '0' && s[j] <= '9' {
				j++
			}
			i = j
		}
	}
	return i
}

func parseNumber(s string) (float64, error) {
	if strings.HasPrefix(s, "0x") || strings.HasPrefix(s, "0X") {
		v, err := strconv.ParseInt(strings.ReplaceAll(s[2:], "_", ""), 16, 64)
		return float64(v), err
	}
	return strconv.ParseFloat(strings.ReplaceAll(s, "_", ""), 64)
}

// ---- parser ----

type pqParser struct {
	toks []pqTok
	pos  int
	src  string
}

// Aggregation operators. topk, bottomk, quantile, count_values, limitk and
// limit_ratio take a parameter before the vector.
var pqAggs = map[string]bool{
	"sum": true, "avg": true, "count": true, "min": true, "max": true, "group": true,
	"stddev": true, "stdvar": true, "topk": true, "bottomk": true, "quantile": true,
	"count_values": true, "limitk": true, "limit_ratio": true,
}

// pqFuncs lists the functions Prometheus 3.x knows, including experimental
// ones, plus holt_winters from Prometheus 2.x.
var pqFuncs = map[string]bool{
	"abs": true, "absent": true, "absent_over_time": true, "acos": true, "acosh": true,
	"asin": true, "asinh": true, "atan": true, "atanh": true, "avg_over_time": true,
	"ceil": true, "changes": true, "clamp": true, "clamp_max": true, "clamp_min": true,
	"cos": true, "cosh": true, "count_over_time": true, "day_of_month": true,
	"day_of_week": true, "day_of_year": true, "days_in_month": true, "deg": true,
	"delta": true, "deriv": true, "double_exponential_smoothing": true, "end": true,
	"exp": true, "first_over_time": true, "floor": true, "histogram_avg": true,
	"histogram_count": true, "histogram_fraction": true, "histogram_quantile": true,
	"histogram_quantiles": true, "histogram_stddev": true, "histogram_stdvar": true,
	"histogram_sum": true, "hour": true, "idelta": true, "increase": true, "info": true,
	"integral": true, "irate": true, "label_join": true, "label_replace": true,
	"last_over_time": true, "ln": true, "log10": true, "log2": true, "mad_over_time": true,
	"max_of": true, "max_over_time": true, "min_of": true, "min_over_time": true,
	"minute": true, "month": true, "pi": true, "predict_linear": true,
	"present_over_time": true, "quantile_over_time": true, "rad": true, "range": true,
	"rate": true, "resets": true, "round": true, "scalar": true, "sgn": true, "sin": true,
	"sinh": true, "sort": true, "sort_by_label": true, "sort_by_label_desc": true,
	"sort_desc": true, "sqrt": true, "start": true, "start_timestamp": true,
	"stddev_over_time": true, "stdvar_over_time": true, "step": true, "sum_over_time": true,
	"tan": true, "tanh": true, "time": true, "timestamp": true,
	"ts_of_first_over_time": true, "ts_of_last_over_time": true,
	"ts_of_max_over_time": true, "ts_of_min_over_time": true, "vector": true, "year": true,
	"holt_winters": true,
}

// Functions that return a result even when their argument has no series,
// or take no series at all.
var pqAlwaysOutput = map[string]bool{
	"absent": true, "absent_over_time": true, "vector": true, "time": true,
	"scalar": true, "pi": true,
}

// Functions that, called without arguments, use vector(time()).
var pqTimeFuncs = map[string]bool{
	"day_of_month": true, "day_of_week": true, "day_of_year": true, "days_in_month": true,
	"hour": true, "minute": true, "month": true, "year": true,
}

func parsePromQL(src string) (n *pqNode, err error) {
	toks, err := pqLex(src)
	if err != nil {
		return nil, err
	}
	p := &pqParser{toks: toks, src: src}
	defer func() {
		if r := recover(); r != nil {
			if pe, ok := r.(pqError); ok {
				n, err = nil, pe
				return
			}
			panic(r)
		}
	}()
	n = p.expr(1)
	if p.peek().kind != tEOF {
		p.fail("unexpected %q", p.peek().val)
	}
	return n, nil
}

type pqError string

func (e pqError) Error() string { return string(e) }

func (p *pqParser) fail(format string, args ...interface{}) {
	t := p.peek()
	msg := fmt.Sprintf(format, args...)
	if t.kind == tEOF {
		msg += " at the end of the expression"
	} else {
		msg += fmt.Sprintf(" at position %d", t.pos)
	}
	panic(pqError(msg))
}

func (p *pqParser) peek() pqTok { return p.toks[p.pos] }

func (p *pqParser) peekAt(k int) pqTok {
	if p.pos+k < len(p.toks) {
		return p.toks[p.pos+k]
	}
	return p.toks[len(p.toks)-1]
}

func (p *pqParser) next() pqTok {
	t := p.toks[p.pos]
	if t.kind != tEOF {
		p.pos++
	}
	return t
}

func (p *pqParser) expect(k pqTokKind, what string) pqTok {
	if p.peek().kind != k {
		p.fail("expected %s", what)
	}
	return p.next()
}

// Binary operator precedence, lowest first.
func binPrec(t pqTok) int {
	if t.kind == tIdent {
		switch strings.ToLower(t.val) {
		case "or":
			return 1
		case "and", "unless":
			return 2
		case "atan2":
			return 5
		}
		return 0
	}
	if t.kind != tOp {
		return 0
	}
	switch t.val {
	case "==", "!=", "<=", "<", ">=", ">", "</", ">/":
		// </ and >/ trim native histograms; they bind like comparisons.
		return 3
	case "+", "-":
		return 4
	case "*", "/", "%":
		return 5
	case "^":
		return 6
	}
	return 0
}

func (p *pqParser) expr(minPrec int) *pqNode {
	lhs := p.unary()
	for {
		t := p.peek()
		prec := binPrec(t)
		if prec == 0 || prec < minPrec {
			return lhs
		}
		p.next()
		bin := &pqNode{Kind: pqBinary, Op: strings.ToLower(t.val), LHS: lhs}
		p.binModifiers(bin)
		if prec == 6 {
			bin.RHS = p.expr(prec) // ^ is right-associative
		} else {
			bin.RHS = p.expr(prec + 1)
		}
		lhs = bin
	}
}

func (p *pqParser) binModifiers(bin *pqNode) {
	if t := p.peek(); t.kind == tIdent && strings.ToLower(t.val) == "bool" {
		p.next()
		bin.ReturnsBool = true
	}
	if t := p.peek(); t.kind == tIdent {
		switch strings.ToLower(t.val) {
		case "on", "ignoring":
			p.next()
			p.labelList()
			if t2 := p.peek(); t2.kind == tIdent {
				switch strings.ToLower(t2.val) {
				case "group_left", "group_right":
					p.next()
					if p.peek().kind == tLParen {
						p.labelList()
					}
				}
			}
		case "group_left", "group_right":
			p.fail("%s needs on or ignoring before it", t.val)
		}
	}
	// Experimental fill modifiers: fill(v), fill_left(v), fill_right(v).
	for {
		t := p.peek()
		if t.kind != tIdent || p.peekAt(1).kind != tLParen {
			return
		}
		switch strings.ToLower(t.val) {
		case "fill", "fill_left", "fill_right":
		default:
			return
		}
		p.next()
		p.next()
		if s := p.peek(); s.kind == tOp && (s.val == "-" || s.val == "+") {
			p.next()
		}
		v := p.next()
		if v.kind != tNumber && !(v.kind == tIdent && (strings.ToLower(v.val) == "inf" || strings.ToLower(v.val) == "nan")) {
			p.fail("expected a number in %s()", t.val)
		}
		p.expect(tRParen, ")")
		bin.Fill = true
	}
}

// labelList reads "(a, b, "c")".
func (p *pqParser) labelList() []string {
	p.expect(tLParen, "(")
	var out []string
	for p.peek().kind != tRParen {
		t := p.next()
		if t.kind != tIdent && t.kind != tString {
			p.fail("expected a label name")
		}
		out = append(out, t.val)
		if p.peek().kind == tComma {
			p.next()
			continue
		}
		if p.peek().kind != tRParen {
			p.fail("expected , or )")
		}
	}
	p.next()
	return out
}

func (p *pqParser) unary() *pqNode {
	if t := p.peek(); t.kind == tOp && (t.val == "-" || t.val == "+") {
		p.next()
		// Unary minus binds looser than ^ : -a^b is -(a^b).
		inner := p.expr(6)
		return &pqNode{Kind: pqUnary, Op: t.val, Inner: inner}
	}
	return p.postfix(p.primary())
}

// postfix reads [range], [range:step], offset and @ after an expression.
func (p *pqParser) postfix(n *pqNode) *pqNode {
	for {
		t := p.peek()
		switch {
		case t.kind == tLBracket:
			p.next()
			if p.bracket() {
				n = &pqNode{Kind: pqSubquery, Inner: n}
			} else {
				if n.Kind != pqSelector || n.Range {
					p.fail("a range is allowed only after a selector")
				}
				n.Range = true
			}
		case t.kind == tIdent && n.Kind == pqSelector && (t.val == "smoothed" || t.val == "anchored"):
			p.next()
		case t.kind == tIdent && strings.ToLower(t.val) == "offset":
			p.next()
			markShifted(n)
			if s := p.peek(); s.kind == tOp && (s.val == "-" || s.val == "+") {
				p.next()
			}
			p.durationExpr()
		case t.kind == tAt:
			p.next()
			markShifted(n)
			s := p.peek()
			switch {
			case s.kind == tIdent && (s.val == "start" || s.val == "end"):
				p.next()
				p.expect(tLParen, "(")
				p.expect(tRParen, ")")
			case s.kind == tOp && (s.val == "-" || s.val == "+"):
				p.next()
				p.expect(tNumber, "a timestamp")
			case s.kind == tNumber || s.kind == tDuration:
				p.next()
			default:
				p.fail("expected a timestamp after @")
			}
		default:
			return n
		}
	}
}

// bracket reads the inside of [...] after the opening bracket and reports
// whether it was a subquery (had a colon).
func (p *pqParser) bracket() bool {
	sub := false
	p.durationExpr()
	if p.peek().kind == tColon {
		sub = true
		p.next()
		if p.peek().kind != tRBracket {
			p.durationExpr()
		}
	}
	p.expect(tRBracket, "]")
	return sub
}

// durationExpr reads a duration, a number of seconds, or a Prometheus 3
// duration expression such as 1m + step() or max_of(range(), 5s).
func (p *pqParser) durationExpr() {
	p.durationTerm()
	for {
		t := p.peek()
		if t.kind == tOp && strings.Contains("+-*/%^", t.val) && len(t.val) == 1 {
			p.next()
			p.durationTerm()
			continue
		}
		return
	}
}

func (p *pqParser) durationTerm() {
	for {
		t := p.peek()
		if t.kind == tOp && (t.val == "-" || t.val == "+") {
			p.next()
			continue
		}
		break
	}
	t := p.peek()
	switch {
	case t.kind == tDuration || t.kind == tNumber:
		p.next()
	case t.kind == tLParen:
		p.next()
		p.durationExpr()
		p.expect(tRParen, ")")
	case t.kind == tIdent && p.peekAt(1).kind == tLParen:
		p.next()
		p.next()
		for p.peek().kind != tRParen {
			p.durationExpr()
			if p.peek().kind == tComma {
				p.next()
				continue
			}
			break
		}
		p.expect(tRParen, ")")
	default:
		p.fail("expected a duration")
	}
}

func (p *pqParser) primary() *pqNode {
	t := p.peek()
	switch t.kind {
	case tNumber:
		p.next()
		return &pqNode{Kind: pqNumber, Text: t.val}
	case tDuration:
		// A bare duration is a number of seconds in Prometheus 3.
		p.next()
		return &pqNode{Kind: pqNumber, Text: t.val}
	case tString:
		p.next()
		return &pqNode{Kind: pqString, Text: t.val}
	case tLParen:
		p.next()
		inner := p.expr(1)
		p.expect(tRParen, ")")
		return &pqNode{Kind: pqParen, Inner: inner}
	case tLBrace:
		return p.selector("")
	case tIdent:
		word := t.val
		lower := strings.ToLower(word)
		if lower == "inf" || lower == "nan" {
			p.next()
			return &pqNode{Kind: pqNumber, Text: word}
		}
		if pqAggs[lower] {
			nt := p.peekAt(1)
			if nt.kind == tLParen || (nt.kind == tIdent && (strings.ToLower(nt.val) == "by" || strings.ToLower(nt.val) == "without")) {
				return p.aggregation(lower)
			}
		}
		if p.peekAt(1).kind == tLParen {
			return p.call(word)
		}
		switch lower {
		case "by", "without", "on", "ignoring", "group_left", "group_right", "bool", "offset", "and", "or", "unless", "atan2":
			if p.peekAt(1).kind != tLBrace {
				p.fail("unexpected keyword %q", word)
			}
		}
		p.next()
		return p.selector(word)
	}
	p.fail("unexpected %q", t.val)
	return nil
}

func (p *pqParser) aggregation(op string) *pqNode {
	p.next()
	if t := p.peek(); t.kind == tIdent {
		p.next()
		p.labelList()
	}
	p.expect(tLParen, "(")
	var args []*pqNode
	for p.peek().kind != tRParen {
		args = append(args, p.expr(1))
		if p.peek().kind == tComma {
			p.next()
			continue
		}
		break
	}
	p.expect(tRParen, ")")
	if t := p.peek(); t.kind == tIdent && (strings.ToLower(t.val) == "by" || strings.ToLower(t.val) == "without") {
		p.next()
		p.labelList()
	}
	if len(args) == 0 {
		p.fail("%s needs an argument", op)
	}
	return &pqNode{Kind: pqAgg, Func: op, Args: args}
}

func (p *pqParser) call(name string) *pqNode {
	if !pqFuncs[name] {
		p.fail("unknown function %q", name)
	}
	p.next()
	p.expect(tLParen, "(")
	var args []*pqNode
	for p.peek().kind != tRParen {
		args = append(args, p.expr(1))
		if p.peek().kind == tComma {
			p.next()
			continue
		}
		break
	}
	p.expect(tRParen, ")")
	return &pqNode{Kind: pqCall, Func: name, Args: args}
}

func (p *pqParser) selector(metric string) *pqNode {
	n := &pqNode{Kind: pqSelector, Metric: metric}
	start := p.peek().pos
	if metric != "" {
		start = p.toks[p.pos-1].pos
	}
	if p.peek().kind == tLBrace {
		p.next()
		for p.peek().kind != tRBrace {
			t := p.next()
			if t.kind != tIdent && t.kind != tString {
				p.fail("expected a label name")
			}
			// A lone quoted string is the metric name (UTF-8 names).
			if t.kind == tString && (p.peek().kind == tComma || p.peek().kind == tRBrace) {
				if n.Metric != "" {
					p.fail("metric name given twice")
				}
				n.Metric = t.val
			} else {
				op := p.next()
				if op.kind != tOp || (op.val != "=" && op.val != "!=" && op.val != "=~" && op.val != "!~") {
					p.fail("expected a label matcher operator")
				}
				v := p.expect(tString, "a quoted label value")
				m := pqMatcher{Name: t.val, Op: op.val, Value: v.val}
				if op.val == "=~" || op.val == "!~" {
					re, err := regexp.Compile("^(?s:" + v.val + ")$")
					if err != nil {
						p.fail("bad regular expression %q", v.val)
					}
					m.re = re
				}
				n.Matchers = append(n.Matchers, m)
			}
			if p.peek().kind == tComma {
				p.next()
				continue
			}
			if p.peek().kind != tRBrace {
				p.fail("expected , or }")
			}
		}
		p.next()
	}
	if n.Metric == "" {
		// A selector needs a name or at least one matcher that doesn't
		// match the empty string.
		ok := false
		for _, m := range n.Matchers {
			if m.Name == "__name__" && m.Op == "=" {
				n.Metric = m.Value
			}
			if !m.matches("") {
				ok = true
			}
		}
		if !ok {
			p.fail("a selector needs a metric name or a matcher that doesn't match the empty string")
		}
	}
	end := len(p.src)
	if p.pos < len(p.toks) {
		end = p.peek().pos
	}
	n.Text = strings.TrimSpace(p.src[start:end])
	return n
}

// parseSeriesLabels reads input series notation: name{label="value", ...}.
func parseSeriesLabels(s string) (map[string]string, error) {
	n, err := parsePromQL(s)
	if err != nil {
		return nil, err
	}
	if n.Kind != pqSelector || n.Range {
		return nil, fmt.Errorf("not a series: %q", s)
	}
	lbls := map[string]string{}
	if n.Metric != "" {
		lbls["__name__"] = n.Metric
	}
	for _, m := range n.Matchers {
		if m.Op != "=" {
			return nil, fmt.Errorf("series %q uses %s; input series take only =", s, m.Op)
		}
		lbls[m.Name] = m.Value
	}
	if lbls["__name__"] == "" {
		return nil, fmt.Errorf("series %q has no metric name", s)
	}
	return lbls, nil
}
