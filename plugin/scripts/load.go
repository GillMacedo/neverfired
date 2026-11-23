package main

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"
)

// untrusted is an error about input that neverfired can't check. It always
// ends the run with exit code 2 and nothing on stdout.
type untrusted struct{ msg string }

func (u *untrusted) Error() string { return u.msg }

func refuse(format string, args ...interface{}) error {
	return &untrusted{fmt.Sprintf(format, args...)}
}

type testFile struct {
	Path         string // as given
	Dir          string
	RulePatterns []rulePattern
	EvalInterval time.Duration
	Groups       []*testGroup
	Rules        []*ruleFile // loaded through rule_files, in order, no repeats
}

type rulePattern struct {
	Pattern string
	Line    int
	Matched int
}

type testGroup struct {
	Index    int
	Name     string
	Line     int
	Interval time.Duration
	Series   []*inputSeries
	Alerts   []*alertTest
	Exprs    []*exprTest
}

func (g *testGroup) label() string {
	if g.Name != "" {
		return fmt.Sprintf("test group %q", g.Name)
	}
	return fmt.Sprintf("test group %d", g.Index+1)
}

type inputSeries struct {
	Text   string
	Line   int
	Labels map[string]string
	// First is the time of the first sample that isn't missing or stale,
	// relative to the start of the test group. HasData is false if there
	// is none.
	First   time.Duration
	HasData bool
}

type alertTest struct {
	Line     int
	EvalTime time.Duration
	Name     string
	Expected int
	Alerts   []expAlert
}

type expAlert struct {
	Line        int
	Labels      map[string]string
	Annotations map[string]string
}

type exprTest struct {
	Line     int
	EvalTime time.Duration
	Expr     string
	Node     *pqNode
	Expected int
}

type ruleFile struct {
	Path  string // as found
	Abs   string
	Kind  string // "rule file" or "PrometheusRule"
	Rules []*rule
}

type rule struct {
	File   *ruleFile
	Group  string
	Line   int
	Alert  string
	Record string
	Expr   string
	Node   *pqNode
	For    time.Duration
	Labels map[string]string
	Annos  map[string]string
}

func (r *rule) name() string {
	if r.Alert != "" {
		return r.Alert
	}
	return r.Record
}

// ---- reading YAML ----

var failedOutput = []*regexp.Regexp{
	regexp.MustCompile(`(?m)^Processing test file: `),
	regexp.MustCompile(`(?m)^\s*FAILED:\s*$`),
	regexp.MustCompile(`(?m)^Checking .*\n\s*FAILED`),
	regexp.MustCompile(`(?m)^[Ee]rror: `),
	regexp.MustCompile(`(?m)^panic: `),
	regexp.MustCompile(`(?m)^# [\w./-]+\n[\w./-]+\.go:\d+`),
	regexp.MustCompile(`(?m)^[\w./-]+\.go:\d+:\d+: `),
	regexp.MustCompile(`(?m)^(make|go|helm|jsonnet|kubectl)(\[\d+\])?: `),
	regexp.MustCompile(`(?m)^(RUNTIME ERROR|STATIC ERROR): `),
}

// readYAML reads one YAML document from a file and checks the file can be
// trusted: present, not empty, text, complete, not a command's error output.
func readYAML(path, what string) (*Node, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, refuse("%s %s doesn't exist", what, path)
		}
		return nil, refuse("can't read %s %s: %v", what, path, err)
	}
	return parseYAML(data, path, what)
}

func parseYAML(data []byte, path, what string) (*Node, error) {
	if len(bytes.TrimSpace(data)) == 0 {
		return nil, refuse("%s %s is empty", what, path)
	}
	if bytes.IndexByte(data, 0) >= 0 || !utf8.Valid(data) {
		return nil, refuse("%s %s is binary, not YAML", what, path)
	}
	for _, re := range failedOutput {
		if re.Match(data) {
			return nil, refuse("%s %s looks like the output of a failed command, not YAML: %q", what, path, firstLine(data, re))
		}
	}
	if data[len(data)-1] != '\n' {
		return nil, refuse("%s %s doesn't end with a newline, so it may be cut off; if the file is complete, add a newline at the end", what, path)
	}
	dec := NewDecoder(bytes.NewReader(data))
	var docs []*Node
	for {
		var n Node
		err := dec.Decode(&n)
		if err != nil {
			if err.Error() == "EOF" {
				break
			}
			return nil, refuse("%s %s isn't valid YAML: %v", what, path, strings.TrimPrefix(err.Error(), "yaml: "))
		}
		docs = append(docs, &n)
	}
	var real []*Node
	for _, d := range docs {
		if d.Kind == DocumentNode && len(d.Content) == 1 && !(d.Content[0].Kind == ScalarNode && d.Content[0].Tag == "!!null") {
			real = append(real, d.Content[0])
		}
	}
	if len(real) == 0 {
		return nil, refuse("%s %s has only comments or empty documents", what, path)
	}
	if len(real) > 1 {
		return &Node{Kind: SequenceNode, Tag: "!!multidoc", Content: real}, nil
	}
	if real[0].Kind == ScalarNode {
		return nil, refuse("%s %s is plain text, not a YAML mapping: %q", what, path, firstLine(data, nil))
	}
	return real[0], nil
}

func firstLine(data []byte, re *regexp.Regexp) string {
	s := string(data)
	if re != nil {
		if loc := re.FindStringIndex(s); loc != nil {
			s = s[loc[0]:]
		}
	}
	s = strings.TrimSpace(s)
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		s = s[:i]
	}
	if len(s) > 80 {
		s = s[:80] + "..."
	}
	return s
}

// fields reads a mapping node into a map of key to value node. Unknown keys
// are refused unless ignoreUnknown is set, the way promtool refuses them.
type yamlCtx struct {
	path          string
	ignoreUnknown bool
}

func (c *yamlCtx) at(n *Node) string {
	return fmt.Sprintf("%s:%d", c.path, n.Line)
}

func (c *yamlCtx) fields(n *Node, what string, known ...string) (map[string]*Node, error) {
	n = deref(n)
	if n.Kind != MappingNode {
		return nil, refuse("%s: %s should be a mapping, found %s", c.at(n), what, kindName(n))
	}
	out := map[string]*Node{}
	for i := 0; i+1 < len(n.Content); i += 2 {
		k, v := n.Content[i], n.Content[i+1]
		if k.Value == "<<" {
			return nil, refuse("%s: YAML merge keys (<<) aren't supported in %s", c.at(k), what)
		}
		ok := false
		for _, kn := range known {
			if kn == k.Value {
				ok = true
			}
		}
		if !ok {
			if c.ignoreUnknown {
				continue
			}
			return nil, refuse("%s: unknown field %q in %s (known: %s); promtool refuses it too, unless you pass --ignore-unknown-fields", c.at(k), k.Value, what, strings.Join(known, ", "))
		}
		if _, dup := out[k.Value]; dup {
			return nil, refuse("%s: field %q appears twice in %s", c.at(k), k.Value, what)
		}
		out[k.Value] = deref(v)
	}
	return out, nil
}

// deref follows a YAML alias to the node it names.
func deref(n *Node) *Node {
	for n != nil && n.Kind == AliasNode && n.Alias != nil {
		n = n.Alias
	}
	return n
}

func kindName(n *Node) string {
	switch n.Kind {
	case MappingNode:
		return "a mapping"
	case SequenceNode:
		return "a list"
	case ScalarNode:
		if n.Tag == "!!null" {
			return "nothing"
		}
		return fmt.Sprintf("%q", n.Value)
	}
	return "something else"
}

func isNull(n *Node) bool {
	return n == nil || (n.Kind == ScalarNode && n.Tag == "!!null")
}

func (c *yamlCtx) list(n *Node, what string) ([]*Node, error) {
	n = deref(n)
	if isNull(n) {
		return nil, nil
	}
	if n.Kind != SequenceNode {
		return nil, refuse("%s: %s should be a list, found %s", c.at(n), what, kindName(n))
	}
	return n.Content, nil
}

func (c *yamlCtx) str(n *Node, what string) (string, error) {
	n = deref(n)
	if n.Kind != ScalarNode || n.Tag == "!!null" {
		return "", refuse("%s: %s should be a string, found %s", c.at(n), what, kindName(n))
	}
	return n.Value, nil
}

// strMap reads a mapping of names to string values, such as labels.
func (c *yamlCtx) strMap(n *Node, what string) (map[string]string, error) {
	n = deref(n)
	out := map[string]string{}
	if isNull(n) {
		return out, nil
	}
	if n.Kind != MappingNode {
		return nil, refuse("%s: %s should be a mapping, found %s", c.at(n), what, kindName(n))
	}
	for i := 0; i+1 < len(n.Content); i += 2 {
		k, v := n.Content[i], deref(n.Content[i+1])
		if k.Kind != ScalarNode || v.Kind != ScalarNode {
			return nil, refuse("%s: %s should map names to strings", c.at(k), what)
		}
		if _, dup := out[k.Value]; dup {
			return nil, refuse("%s: %q appears twice in %s", c.at(k), k.Value, what)
		}
		if v.Tag == "!!null" {
			out[k.Value] = ""
			continue
		}
		out[k.Value] = v.Value
	}
	return out, nil
}

func (c *yamlCtx) duration(n *Node, what string) (time.Duration, error) {
	s, err := c.str(n, what)
	if err != nil {
		return 0, err
	}
	d, err := parseDuration(s)
	if err != nil {
		return 0, refuse("%s: %s: %v", c.at(n), what, err)
	}
	return d, nil
}

var promDurationRe = regexp.MustCompile(`^(([0-9]+)y)?(([0-9]+)w)?(([0-9]+)d)?(([0-9]+)h)?(([0-9]+)m)?(([0-9]+)s)?(([0-9]+)ms)?$`)

// parseDuration reads a Prometheus duration such as 1h30m, 5m or 0.
func parseDuration(s string) (time.Duration, error) {
	if s == "0" {
		return 0, nil
	}
	m := promDurationRe.FindStringSubmatch(s)
	if s == "" || m == nil {
		return 0, fmt.Errorf("%q is not a duration (examples: 30s, 5m, 1h30m)", s)
	}
	units := []time.Duration{365 * 24 * time.Hour, 7 * 24 * time.Hour, 24 * time.Hour, time.Hour, time.Minute, time.Second, time.Millisecond}
	var d time.Duration
	for i, u := range units {
		v := m[2+2*i]
		if v == "" {
			continue
		}
		n, err := strconv.ParseInt(v, 10, 64)
		if err != nil || n > int64((1<<63-1)/u) {
			return 0, fmt.Errorf("%q is too long", s)
		}
		d += time.Duration(n) * u
	}
	return d, nil
}

func fmtDuration(d time.Duration) string {
	if d == 0 {
		return "0s"
	}
	var b strings.Builder
	for _, u := range []struct {
		d time.Duration
		s string
	}{{24 * time.Hour, "d"}, {time.Hour, "h"}, {time.Minute, "m"}, {time.Second, "s"}, {time.Millisecond, "ms"}} {
		if d >= u.d {
			fmt.Fprintf(&b, "%d%s", d/u.d, u.s)
			d %= u.d
		}
	}
	return b.String()
}

// ---- test files ----

func loadTestFile(path string, ignoreUnknown bool, cache map[string]*ruleFile) (*testFile, error) {
	root, err := readYAML(path, "test file")
	if err != nil {
		return nil, err
	}
	c := &yamlCtx{path: path, ignoreUnknown: ignoreUnknown}
	if root.Tag == "!!multidoc" {
		return nil, refuse("test file %s has more than one YAML document; promtool reads only the first, so the rest would never run", path)
	}
	if root.Kind == MappingNode && hasKey(root, "groups") && !hasKey(root, "tests") {
		return nil, refuse("%s is a rule file, not a promtool test file; pass it with --rules", path)
	}
	top, err := c.fields(root, "the test file", "rule_files", "evaluation_interval", "fuzzy_compare", "group_eval_order", "tests")
	if err != nil {
		return nil, err
	}
	tf := &testFile{Path: path, Dir: filepath.Dir(path), EvalInterval: time.Minute}
	if n, ok := top["evaluation_interval"]; ok {
		if tf.EvalInterval, err = c.duration(n, "evaluation_interval"); err != nil {
			return nil, err
		}
		if tf.EvalInterval <= 0 {
			return nil, refuse("%s: evaluation_interval must be more than 0", c.at(n))
		}
	}
	rfs, err := c.list(top["rule_files"], "rule_files")
	if err != nil {
		return nil, err
	}
	for _, n := range rfs {
		s, err := c.str(n, "a rule_files entry")
		if err != nil {
			return nil, err
		}
		tf.RulePatterns = append(tf.RulePatterns, rulePattern{Pattern: s, Line: n.Line})
	}
	groups, err := c.list(top["tests"], "tests")
	if err != nil {
		return nil, err
	}
	if len(groups) == 0 {
		return nil, refuse("test file %s has no tests; promtool passes it without checking anything", path)
	}
	for i, gn := range groups {
		g, err := loadGroup(c, gn, i, tf.EvalInterval)
		if err != nil {
			return nil, err
		}
		tf.Groups = append(tf.Groups, g)
	}
	// Resolve rule_files relative to the test file, as promtool does.
	seen := map[string]bool{}
	for i := range tf.RulePatterns {
		rp := &tf.RulePatterns[i]
		pat := rp.Pattern
		if !filepath.IsAbs(pat) {
			pat = filepath.Join(tf.Dir, pat)
		}
		matches, err := filepath.Glob(pat)
		if err != nil {
			return nil, refuse("%s:%d: rule_files pattern %q is not a valid glob: %v", path, rp.Line, rp.Pattern, err)
		}
		sort.Strings(matches)
		for _, m := range matches {
			if st, err := os.Stat(m); err == nil && st.IsDir() {
				continue
			}
			rp.Matched++
			rf, err := loadRuleFileCached(m, ignoreUnknown, cache, false)
			if err != nil {
				return nil, err
			}
			if !seen[rf.Abs] {
				seen[rf.Abs] = true
				tf.Rules = append(tf.Rules, rf)
			}
		}
	}
	return tf, nil
}

func hasKey(n *Node, key string) bool {
	for i := 0; i+1 < len(n.Content); i += 2 {
		if n.Content[i].Value == key {
			return true
		}
	}
	return false
}

func loadGroup(c *yamlCtx, gn *Node, idx int, evalInterval time.Duration) (*testGroup, error) {
	f, err := c.fields(gn, "a test group", "interval", "input_series", "name", "start_timestamp", "alert_rule_test", "promql_expr_test", "external_labels", "external_url")
	if err != nil {
		return nil, err
	}
	g := &testGroup{Index: idx, Line: gn.Line, Interval: evalInterval}
	if n, ok := f["name"]; ok && !isNull(n) {
		if g.Name, err = c.str(n, "name"); err != nil {
			return nil, err
		}
	}
	if n, ok := f["interval"]; ok {
		if g.Interval, err = c.duration(n, "interval"); err != nil {
			return nil, err
		}
		if g.Interval <= 0 {
			return nil, refuse("%s: interval must be more than 0", c.at(n))
		}
	}
	series, err := c.list(f["input_series"], "input_series")
	if err != nil {
		return nil, err
	}
	for _, sn := range series {
		sf, err := c.fields(sn, "an input series", "series", "values")
		if err != nil {
			return nil, err
		}
		if sf["series"] == nil {
			return nil, refuse("%s: input series has no series field (is the file cut off?)", c.at(sn))
		}
		if sf["values"] == nil {
			return nil, refuse("%s: input series has no values field (is the file cut off?)", c.at(sn))
		}
		text, err := c.str(sf["series"], "series")
		if err != nil {
			return nil, err
		}
		lbls, err := parseSeriesLabels(text)
		if err != nil {
			return nil, refuse("%s: can't read series %q: %v", c.at(sf["series"]), text, err)
		}
		vals, err := c.str(sf["values"], "values")
		if err != nil {
			return nil, err
		}
		first, has, err := firstSample(vals)
		if err != nil {
			return nil, refuse("%s: can't read values %q: %v", c.at(sf["values"]), vals, err)
		}
		g.Series = append(g.Series, &inputSeries{Text: text, Line: sn.Line, Labels: lbls, First: time.Duration(first) * g.Interval, HasData: has})
	}
	alerts, err := c.list(f["alert_rule_test"], "alert_rule_test")
	if err != nil {
		return nil, err
	}
	for _, an := range alerts {
		af, err := c.fields(an, "an alert_rule_test case", "eval_time", "alertname", "exp_alerts")
		if err != nil {
			return nil, err
		}
		if af["alertname"] == nil {
			return nil, refuse("%s: alert_rule_test case has no alertname (is the file cut off?)", c.at(an))
		}
		if af["eval_time"] == nil {
			return nil, refuse("%s: alert_rule_test case has no eval_time (is the file cut off?)", c.at(an))
		}
		at := &alertTest{Line: an.Line}
		if at.Name, err = c.str(af["alertname"], "alertname"); err != nil {
			return nil, err
		}
		if at.EvalTime, err = c.duration(af["eval_time"], "eval_time"); err != nil {
			return nil, err
		}
		exp, err := c.list(af["exp_alerts"], "exp_alerts")
		if err != nil {
			return nil, err
		}
		for _, e := range exp {
			ef, err := c.fields(e, "an expected alert", "exp_labels", "exp_annotations")
			if err != nil {
				return nil, err
			}
			ea := expAlert{Line: e.Line}
			if ea.Labels, err = c.strMap(ef["exp_labels"], "exp_labels"); err != nil {
				return nil, err
			}
			if ea.Annotations, err = c.strMap(ef["exp_annotations"], "exp_annotations"); err != nil {
				return nil, err
			}
			at.Alerts = append(at.Alerts, ea)
		}
		at.Expected = len(exp)
		g.Alerts = append(g.Alerts, at)
	}
	exprs, err := c.list(f["promql_expr_test"], "promql_expr_test")
	if err != nil {
		return nil, err
	}
	for _, en := range exprs {
		ef, err := c.fields(en, "a promql_expr_test case", "expr", "eval_time", "exp_samples")
		if err != nil {
			return nil, err
		}
		if ef["expr"] == nil {
			return nil, refuse("%s: promql_expr_test case has no expr (is the file cut off?)", c.at(en))
		}
		if ef["eval_time"] == nil {
			return nil, refuse("%s: promql_expr_test case has no eval_time (is the file cut off?)", c.at(en))
		}
		et := &exprTest{Line: en.Line}
		if et.Expr, err = c.str(ef["expr"], "expr"); err != nil {
			return nil, err
		}
		if et.Node, err = parsePromQL(et.Expr); err != nil {
			return nil, refuse("%s: can't parse expr %q: %v", c.at(ef["expr"]), et.Expr, err)
		}
		if et.EvalTime, err = c.duration(ef["eval_time"], "eval_time"); err != nil {
			return nil, err
		}
		exp, err := c.list(ef["exp_samples"], "exp_samples")
		if err != nil {
			return nil, err
		}
		for _, e := range exp {
			if _, err := c.fields(e, "an expected sample", "labels", "value", "histogram"); err != nil {
				return nil, err
			}
		}
		et.Expected = len(exp)
		g.Exprs = append(g.Exprs, et)
	}
	if len(g.Alerts) == 0 && len(g.Exprs) == 0 {
		return nil, refuse("%s: %s has no alert_rule_test or promql_expr_test cases, so it checks nothing (is the file cut off?)", c.at(gn), g.label())
	}
	return g, nil
}

// firstSample returns the index of the first sample that isn't missing or
// stale in promtool's expanding notation, and whether there is one.
func firstSample(vals string) (int, bool, error) {
	toks, err := splitValues(vals)
	if err != nil {
		return 0, false, err
	}
	idx, first, has := 0, 0, false
	for _, t := range toks {
		n, real, err := valueToken(t)
		if err != nil {
			return 0, false, err
		}
		if real && !has {
			first, has = idx, true
		}
		idx += n
	}
	return first, has, nil
}

// splitValues splits on spaces, keeping {{ ... }} histograms whole.
func splitValues(s string) ([]string, error) {
	var out []string
	var cur strings.Builder
	depth := 0
	for i := 0; i < len(s); i++ {
		ch := s[i]
		switch {
		case strings.HasPrefix(s[i:], "{{"):
			depth++
			cur.WriteString("{{")
			i++
		case strings.HasPrefix(s[i:], "}}"):
			if depth == 0 {
				return nil, fmt.Errorf("}} without {{")
			}
			depth--
			cur.WriteString("}}")
			i++
		case (ch == ' ' || ch == '\t' || ch == '\n') && depth == 0:
			if cur.Len() > 0 {
				out = append(out, cur.String())
				cur.Reset()
			}
		default:
			cur.WriteByte(ch)
		}
	}
	if depth != 0 {
		return nil, fmt.Errorf("{{ without }}")
	}
	if cur.Len() > 0 {
		out = append(out, cur.String())
	}
	return out, nil
}

var (
	numRe     = `[-+]?(?:[0-9]*\.?[0-9]+(?:[eE][-+]?[0-9]+)?|0[xX][0-9a-fA-F]+|[Ii]nf|NaN|nan)`
	floatTok  = regexp.MustCompile(`^(` + numRe + `)$`)
	expandTok = regexp.MustCompile(`^(` + numRe + `)(?:([-+])(` + `[0-9]*\.?[0-9]+(?:[eE][-+]?[0-9]+)?` + `))?x([0-9]+)$`)
	histTok   = regexp.MustCompile(`^\{\{[^{}]*\}\}(?:(?:[-+]\{\{[^{}]*\}\})?x([0-9]+))?$`)
	missTok   = regexp.MustCompile(`^_(?:x([0-9]+))?$`)
)

// valueToken returns how many samples a token stands for and whether they
// are real (not missing or stale).
func valueToken(t string) (int, bool, error) {
	if m := missTok.FindStringSubmatch(t); m != nil {
		if m[1] == "" {
			return 1, false, nil
		}
		n, _ := strconv.Atoi(m[1])
		return n, false, nil
	}
	if t == "stale" {
		return 1, false, nil
	}
	if floatTok.MatchString(t) {
		return 1, true, nil
	}
	if m := expandTok.FindStringSubmatch(t); m != nil {
		n, err := strconv.Atoi(m[4])
		if err != nil {
			return 0, false, fmt.Errorf("bad count in %q", t)
		}
		return n + 1, true, nil
	}
	if m := histTok.FindStringSubmatch(t); m != nil {
		if m[1] == "" {
			return 1, true, nil
		}
		n, _ := strconv.Atoi(m[1])
		return n + 1, true, nil
	}
	return 0, false, fmt.Errorf("%q is not a value, an expanding value such as 1+2x10, _ or stale", t)
}

// ---- rule files ----

func loadRuleFileCached(path string, ignoreUnknown bool, cache map[string]*ruleFile, allowCRD bool) (*ruleFile, error) {
	abs, err := filepath.Abs(path)
	if err != nil {
		return nil, refuse("can't resolve %s: %v", path, err)
	}
	if rf, ok := cache[abs]; ok {
		if rf.Kind == "PrometheusRule" && !allowCRD {
			return nil, refuse("%s is a PrometheusRule, which promtool can't load from rule_files; extract its spec into a plain rule file", path)
		}
		return rf, nil
	}
	rf, err := loadRuleFile(path, abs, ignoreUnknown, allowCRD)
	if err != nil {
		return nil, err
	}
	cache[abs] = rf
	return rf, nil
}

func loadRuleFile(path, abs string, ignoreUnknown, allowCRD bool) (*ruleFile, error) {
	root, err := readYAML(path, "rule file")
	if err != nil {
		return nil, err
	}
	c := &yamlCtx{path: path, ignoreUnknown: ignoreUnknown}
	rf := &ruleFile{Path: path, Abs: abs, Kind: "rule file"}
	docs := []*Node{root}
	if root.Tag == "!!multidoc" {
		docs = root.Content
	}
	for _, doc := range docs {
		if doc.Kind == MappingNode && hasKey(doc, "kind") {
			if !allowCRD {
				return nil, refuse("%s is a Kubernetes object, which promtool can't load from rule_files; extract the PrometheusRule spec into a plain rule file", path)
			}
			if err := loadCRD(c, doc, rf); err != nil {
				return nil, err
			}
			rf.Kind = "PrometheusRule"
			continue
		}
		if len(docs) > 1 {
			return nil, refuse("rule file %s has more than one YAML document; promtool reads only the first", path)
		}
		if doc.Kind == MappingNode && hasKey(doc, "tests") {
			return nil, refuse("%s is a promtool test file, not a rule file", path)
		}
		top, err := c.fields(doc, "the rule file", "groups")
		if err != nil {
			return nil, err
		}
		if err := loadGroups(c, top["groups"], rf); err != nil {
			return nil, err
		}
	}
	if len(rf.Rules) == 0 {
		return nil, refuse("rule file %s has no rules (is it cut off, or the wrong file?)", path)
	}
	return rf, nil
}

func loadCRD(c *yamlCtx, doc *Node, rf *ruleFile) error {
	top, err := yamlCtxLenient(c).fields(doc, "a Kubernetes object", "apiVersion", "kind", "metadata", "spec")
	if err != nil {
		return err
	}
	kind, _ := c.str(top["kind"], "kind")
	if kind != "PrometheusRule" {
		return refuse("%s: %s is a %s, not a PrometheusRule", c.at(doc), c.path, kind)
	}
	if top["spec"] == nil {
		return refuse("%s: PrometheusRule has no spec (is the file cut off?)", c.at(doc))
	}
	spec, err := yamlCtxLenient(c).fields(top["spec"], "the PrometheusRule spec", "groups")
	if err != nil {
		return err
	}
	return loadGroups(c, spec["groups"], rf)
}

func yamlCtxLenient(c *yamlCtx) *yamlCtx {
	return &yamlCtx{path: c.path, ignoreUnknown: true}
}

func loadGroups(c *yamlCtx, n *Node, rf *ruleFile) error {
	if isNull(n) {
		return refuse("%s has no groups", c.path)
	}
	groups, err := c.list(n, "groups")
	if err != nil {
		return err
	}
	for _, gn := range groups {
		gf, err := c.fields(gn, "a rule group", "name", "interval", "query_offset", "limit", "rules", "labels")
		if err != nil {
			return err
		}
		if gf["name"] == nil {
			return refuse("%s: rule group has no name (is the file cut off?)", c.at(gn))
		}
		gname, err := c.str(gf["name"], "group name")
		if err != nil {
			return err
		}
		rules, err := c.list(gf["rules"], "rules")
		if err != nil {
			return err
		}
		for _, rn := range rules {
			rfld, err := c.fields(rn, "a rule", "alert", "record", "expr", "for", "keep_firing_for", "labels", "annotations")
			if err != nil {
				return err
			}
			r := &rule{File: rf, Group: gname, Line: rn.Line}
			if n := rfld["alert"]; n != nil {
				if r.Alert, err = c.str(n, "alert"); err != nil {
					return err
				}
			}
			if n := rfld["record"]; n != nil {
				if r.Record, err = c.str(n, "record"); err != nil {
					return err
				}
			}
			if (r.Alert == "") == (r.Record == "") {
				return refuse("%s: a rule needs exactly one of alert and record (is the file cut off?)", c.at(rn))
			}
			if rfld["expr"] == nil {
				return refuse("%s: rule %s has no expr (is the file cut off?)", c.at(rn), r.name())
			}
			if r.Expr, err = c.str(rfld["expr"], "expr"); err != nil {
				return err
			}
			if r.Node, err = parsePromQL(r.Expr); err != nil {
				return refuse("%s: can't parse the expr of %s: %v", c.at(rfld["expr"]), r.name(), err)
			}
			if r.Labels, err = c.strMap(rfld["labels"], "labels"); err != nil {
				return err
			}
			if r.Annos, err = c.strMap(rfld["annotations"], "annotations"); err != nil {
				return err
			}
			if n := rfld["for"]; n != nil {
				if r.Record != "" {
					return refuse("%s: recording rule %s has a for field", c.at(n), r.Record)
				}
				if r.For, err = c.duration(n, "for"); err != nil {
					return err
				}
			}
			rf.Rules = append(rf.Rules, r)
		}
	}
	return nil
}
