package main

import (
	"fmt"
	"sort"
	"strings"
	"time"
)

type finding struct {
	Code  string `json:"code"`
	Level string `json:"level"` // error, warn, note
	File  string `json:"file"`
	Line  int    `json:"line,omitempty"`
	Where string `json:"where,omitempty"`
	Msg   string `json:"message"`
	Fix   string `json:"fix,omitempty"`
}

var levelRank = map[string]int{"note": 1, "warn": 2, "error": 3}

// coverage is the state of one alerting rule across all test files.
type coverage struct {
	Alert   string `json:"alert"`
	File    string `json:"file"`
	Line    int    `json:"line"`
	Status  string `json:"status"` // fires, quiet-only, untested, not-loaded
	Firing  int    `json:"tests_expecting_firing"`
	Quiet   int    `json:"tests_expecting_quiet"`
	Vacuous int    `json:"quiet_tests_that_cannot_fail"`
	Via     string `json:"tested_copy,omitempty"`
	Differs bool   `json:"tested_copy_differs,omitempty"`
}

type report struct {
	TestFiles []string    `json:"test_files"`
	RuleFiles []string    `json:"rule_files"`
	Findings  []finding   `json:"findings"`
	Coverage  []*coverage `json:"coverage"`
}

type tally struct{ firing, quiet, vacuous int }

func analyze(tests []*testFile, extra []*ruleFile) *report {
	rep := &report{Findings: []finding{}, Coverage: []*coverage{}}
	counts := map[*rule]*tally{}
	loaded := map[string]bool{}
	var universe []*ruleFile
	addFile := func(rf *ruleFile) {
		for _, u := range universe {
			if u.Abs == rf.Abs {
				return
			}
		}
		universe = append(universe, rf)
	}
	for _, tf := range tests {
		rep.TestFiles = append(rep.TestFiles, tf.Path)
		for _, rf := range tf.Rules {
			loaded[rf.Abs] = true
			addFile(rf)
		}
		checkTestFile(tf, rep, counts)
	}
	for _, rf := range extra {
		addFile(rf)
	}
	for _, rf := range universe {
		rep.RuleFiles = append(rep.RuleFiles, rf.Path)
	}

	// Alerts in files some test loads, by name, for matching copies.
	byName := map[string][]*rule{}
	for _, rf := range universe {
		if !loaded[rf.Abs] {
			continue
		}
		for _, r := range rf.Rules {
			if r.Alert != "" {
				byName[r.Alert] = append(byName[r.Alert], r)
			}
		}
	}
	for _, rf := range universe {
		for _, r := range rf.Rules {
			if r.Alert == "" {
				continue
			}
			cv := &coverage{Alert: r.Alert, File: rf.Path, Line: r.Line}
			where := fmt.Sprintf("alert %s", r.Alert)
			if loaded[rf.Abs] {
				t := counts[r]
				if t == nil {
					t = &tally{}
				}
				cv.Firing, cv.Quiet, cv.Vacuous = t.firing, t.quiet, t.vacuous
				cv.Status = statusOf(t)
			} else {
				var same, other []*rule
				for _, c := range byName[r.Alert] {
					if normExpr(c.Expr) == normExpr(r.Expr) && c.For == r.For {
						same = append(same, c)
					} else {
						other = append(other, c)
					}
				}
				copies := same
				if len(copies) == 0 {
					copies = other
					cv.Differs = len(other) > 0
				}
				if len(copies) == 0 {
					cv.Status = "not-loaded"
					rep.add(finding{Code: "NF203", Level: "warn", File: rf.Path, Line: r.Line, Where: where,
						Msg: fmt.Sprintf("no test file loads this alert: %s isn't in any test's rule_files, and no tested rule file has an alert named %s", rf.Path, r.Alert),
						Fix: "add the file to a test's rule_files and write a test that expects the alert to fire"})
					rep.Coverage = append(rep.Coverage, cv)
					continue
				}
				best := copies[0]
				t := &tally{}
				for _, c := range copies {
					if ct := counts[c]; ct != nil {
						t.firing += ct.firing
						t.quiet += ct.quiet
						t.vacuous += ct.vacuous
					}
				}
				cv.Firing, cv.Quiet, cv.Vacuous = t.firing, t.quiet, t.vacuous
				cv.Status = statusOf(t)
				cv.Via = fmt.Sprintf("%s:%d", best.File.Path, best.Line)
				if cv.Differs {
					rep.add(finding{Code: "NF204", Level: "warn", File: rf.Path, Line: r.Line, Where: where,
						Msg: fmt.Sprintf("the tests check a different version of this alert: %s:%d has %s, and this file has %s", best.File.Path, best.Line, describeRule(best), describeRule(r)),
						Fix: "regenerate the tested rule file from this one, or point the tests at this file"})
				}
				// Coverage findings for copies are reported on the tested copy.
				rep.Coverage = append(rep.Coverage, cv)
				continue
			}
			switch cv.Status {
			case "untested":
				rep.add(finding{Code: "NF201", Level: "warn", File: rf.Path, Line: r.Line, Where: where,
					Msg: fmt.Sprintf("no test names this alert, though %s loads its file", loaderNames(tests, rf)),
					Fix: "add an alert_rule_test case whose input makes the alert fire, with its expected labels"})
			case "quiet-only":
				msg := "no test expects this alert to fire; 1 test case expects it to stay quiet"
				if cv.Quiet != 1 {
					msg = fmt.Sprintf("no test expects this alert to fire; %d test cases expect it to stay quiet", cv.Quiet)
				}
				if cv.Vacuous > 0 {
					msg += fmt.Sprintf(", and %d of them can't fail (see above)", cv.Vacuous)
				}
				rep.add(finding{Code: "NF202", Level: "warn", File: rf.Path, Line: r.Line, Where: where,
					Msg: msg + ", so nothing shows the expression ever matches",
					Fix: "add an alert_rule_test case with exp_alerts that makes the alert fire"})
			}
			rep.Coverage = append(rep.Coverage, cv)
		}
	}
	sortFindings(rep.Findings)
	return rep
}

func describeRule(r *rule) string {
	s := fmt.Sprintf("expr %q", normExpr(r.Expr))
	if r.For > 0 {
		s += " for " + fmtDuration(r.For)
	}
	return s
}

func statusOf(t *tally) string {
	switch {
	case t.firing > 0:
		return "fires"
	case t.quiet > 0:
		return "quiet-only"
	}
	return "untested"
}

func normExpr(s string) string { return strings.Join(strings.Fields(s), " ") }

func loaderNames(tests []*testFile, rf *ruleFile) string {
	var names []string
	for _, tf := range tests {
		for _, r := range tf.Rules {
			if r.Abs == rf.Abs {
				names = append(names, tf.Path)
			}
		}
	}
	return strings.Join(names, ", ")
}

func (rep *report) add(f finding) { rep.Findings = append(rep.Findings, f) }

func sortFindings(fs []finding) {
	sort.SliceStable(fs, func(i, j int) bool {
		if levelRank[fs[i].Level] != levelRank[fs[j].Level] {
			return levelRank[fs[i].Level] > levelRank[fs[j].Level]
		}
		if fs[i].File != fs[j].File {
			return fs[i].File < fs[j].File
		}
		return fs[i].Line < fs[j].Line
	})
}

// ---- one test file ----

func checkTestFile(tf *testFile, rep *report, counts map[*rule]*tally) {
	for _, rp := range tf.RulePatterns {
		if rp.Matched == 0 {
			rep.add(finding{Code: "NF101", Level: "error", File: tf.Path, Line: rp.Line, Where: "rule_files",
				Msg: fmt.Sprintf("rule_files entry %q matches no file (looked in %s); promtool only prints a warning and still reports SUCCESS", rp.Pattern, tf.Dir),
				Fix: "fix the path; rule_files are relative to the test file's directory"})
		}
	}
	alerts := map[string][]*rule{}
	var records []*rule
	var all []*rule
	for _, rf := range tf.Rules {
		for _, r := range rf.Rules {
			all = append(all, r)
			if r.Alert != "" {
				alerts[r.Alert] = append(alerts[r.Alert], r)
			} else {
				records = append(records, r)
			}
		}
	}
	for _, g := range tf.Groups {
		fc := &feedCtx{g: g, records: records, visiting: map[*rule]bool{}}
		// The latest time this group checks each alert. A quiet check
		// before for: elapses, next to a later check of the same alert,
		// tests the pending period on purpose.
		lastCheck := map[string]time.Duration{}
		for _, at := range g.Alerts {
			if at.EvalTime > lastCheck[at.Name] {
				lastCheck[at.Name] = at.EvalTime
			}
		}
		for _, at := range g.Alerts {
			where := fmt.Sprintf("%s, %s at %s", g.label(), at.Name, fmtDuration(at.EvalTime))
			rules := alerts[at.Name]
			if len(rules) == 0 {
				msg := fmt.Sprintf("no alert named %s is defined in the rule files this test loads", at.Name)
				if at.Expected == 0 {
					msg += ", so this test expects no alert from a rule that doesn't exist and passes whatever the rules do"
				} else {
					msg += ", so promtool fails this test"
				}
				if s := suggest(at.Name, alerts); s != "" {
					msg += fmt.Sprintf(" (did you mean %s?)", s)
				}
				rep.add(finding{Code: "NF102", Level: "error", File: tf.Path, Line: at.Line, Where: where, Msg: msg,
					Fix: "correct the alertname, or add the rule file that defines it to rule_files"})
				continue
			}
			if at.Expected > 0 {
				for _, r := range rules {
					tallyOf(counts, r).firing++
				}
				if len(rules) == 1 {
					checkExpected(tf, g, at, rules[0], all, where, rep)
				}
				continue
			}
			// A test that expects the alert to stay quiet. It shows something
			// only if at least one rule with this name could have fired.
			var early []*rule
			var starved []*rule
			var misses []miss
			ok := 0
			for _, r := range rules {
				tallyOf(counts, r).quiet++
				if r.For > at.EvalTime {
					if lastCheck[at.Name] >= r.For {
						ok++
						continue
					}
					early = append(early, r)
					continue
				}
				empty, ms := fc.empty(r.Node, at.EvalTime)
				if empty {
					starved = append(starved, r)
					misses = append(misses, ms...)
					continue
				}
				ok++
			}
			if ok > 0 {
				continue
			}
			for _, r := range rules {
				tallyOf(counts, r).vacuous++
			}
			if len(starved) == 0 {
				rep.add(finding{Code: "NF103", Level: "warn", File: tf.Path, Line: at.Line, Where: where,
					Msg: fmt.Sprintf("this test expects no alert at %s, but the alert has for: %s, so it can't fire before %s whatever the input; the test checks only the for duration, never the expression, and this group has no later check of the alert",
						fmtDuration(at.EvalTime), fmtDuration(early[0].For), fmtDuration(early[0].For)),
					Fix: fmt.Sprintf("move eval_time to %s or later, after the input has stayed in the state you want to show doesn't alert", fmtDuration(early[0].For))})
				continue
			}
			why, mistake := describeMisses(misses, at.EvalTime)
			extra := ""
			if len(early) > 0 {
				extra = fmt.Sprintf(" (another rule with this name has for: %s, longer than eval_time)", fmtDuration(early[0].For))
			}
			if mistake {
				rep.add(finding{Code: "NF104", Level: "error", File: tf.Path, Line: at.Line, Where: where,
					Msg: fmt.Sprintf("this test expects no alert, but the alert's expression can't return anything at %s: %s; the test passes whatever values the series have%s",
						fmtDuration(at.EvalTime), why, extra),
					Fix: "fix the series' labels or sample times so the expression reads them, with values that stay under the threshold"})
				continue
			}
			rep.add(finding{Code: "NF107", Level: "warn", File: tf.Path, Line: at.Line, Where: where,
				Msg: fmt.Sprintf("this test expects no alert, but %s, so the expression returns nothing at %s whatever the other series hold; the test only shows the alert stays quiet without that metric%s",
					why, fmtDuration(at.EvalTime), extra),
				Fix: "if the test is about the metric being missing, this is expected; otherwise add input series for it"})
		}
		for _, et := range g.Exprs {
			if et.Expected > 0 {
				continue
			}
			empty, ms := fc.empty(et.Node, et.EvalTime)
			if !empty {
				continue
			}
			why, mistake := describeMisses(ms, et.EvalTime)
			where := fmt.Sprintf("%s, expr at %s", g.label(), fmtDuration(et.EvalTime))
			if mistake {
				rep.add(finding{Code: "NF105", Level: "error", File: tf.Path, Line: et.Line, Where: where,
					Msg: fmt.Sprintf("this test expects no samples, but the expression can't return anything at %s: %s; the test passes whatever values the series have", fmtDuration(et.EvalTime), why),
					Fix: "fix the series' labels or sample times so the expression reads them"})
				continue
			}
			rep.add(finding{Code: "NF108", Level: "warn", File: tf.Path, Line: et.Line, Where: where,
				Msg: fmt.Sprintf("this test expects no samples, but %s, so the expression returns nothing at %s whatever the other series hold", why, fmtDuration(et.EvalTime)),
				Fix: "if the test is about the metric being missing, this is expected; otherwise add input series for it"})
		}
		if len(all) == 0 {
			continue
		}
		for _, s := range g.Series {
			if seriesRead(s, all, g.Exprs) {
				continue
			}
			rep.add(finding{Code: "NF301", Level: "note", File: tf.Path, Line: s.Line, Where: g.label(),
				Msg: fmt.Sprintf("input series %s has a metric name that no rule in the loaded files and no expr in this group reads; a typo in the name would look like this", s.Text),
				Fix: "fix the metric name, or remove the series"})
		}
	}
}

// checkExpected reports expected alerts promtool can't match, judged only
// from what is certain: labels and annotations the rule sets without a
// template, and label names nothing could produce.
func checkExpected(tf *testFile, g *testGroup, at *alertTest, r *rule, all []*rule, where string, rep *report) {
	possible := map[string]bool{}
	for _, s := range g.Series {
		for k := range s.Labels {
			possible[k] = true
		}
	}
	for _, x := range all {
		// Labels set by recording rules reach the series they record; an
		// alerting rule's labels reach only its own alerts.
		if x.Record != "" {
			for k := range x.Labels {
				possible[k] = true
			}
		}
		for _, n := range madeLabels(x.Node) {
			possible[n] = true
		}
	}
	for _, e := range g.Exprs {
		for _, n := range madeLabels(e.Node) {
			possible[n] = true
		}
	}
	add := func(line int, msg string) {
		rep.add(finding{Code: "NF106", Level: "error", File: tf.Path, Line: line, Where: where,
			Msg: msg + "; promtool compares labels and annotations exactly, so this test fails (or the file is cut off)",
			Fix: "make exp_labels and exp_annotations match what the rule produces, or fix the rule"})
	}
	for _, ea := range at.Alerts {
		for _, k := range sortedKeys(r.Labels) {
			v := r.Labels[k]
			if strings.Contains(v, "{{") || v == "" {
				continue
			}
			got, ok := ea.Labels[k]
			if !ok {
				add(ea.Line, fmt.Sprintf("the expected alert has no %s label, but the rule always sets %s=%q", k, k, v))
			} else if got != v {
				add(ea.Line, fmt.Sprintf("the expected alert has %s=%q, but the rule always sets %s=%q", k, got, k, v))
			}
		}
		for _, k := range sortedKeys(ea.Labels) {
			if k == "alertname" || possible[k] {
				continue
			}
			if _, ok := r.Labels[k]; ok {
				continue
			}
			add(ea.Line, fmt.Sprintf("the expected alert has a %s label, but neither this rule, the input series, a recording rule label nor a label_replace in the loaded rules makes one", k))
		}
		for _, k := range sortedKeys(ea.Annotations) {
			if _, ok := r.Annos[k]; !ok {
				add(ea.Line, fmt.Sprintf("the expected alert has a %s annotation, which the rule doesn't define", k))
			}
		}
		for _, k := range sortedKeys(r.Annos) {
			v := r.Annos[k]
			if strings.Contains(v, "{{") || v == "" {
				continue
			}
			got, ok := ea.Annotations[k]
			if !ok {
				add(ea.Line, fmt.Sprintf("the expected alert has no %s annotation, but the rule always sets it to %q", k, v))
			} else if got != v {
				add(ea.Line, fmt.Sprintf("the expected alert has annotation %s=%q, but the rule always sets %q", k, got, v))
			}
		}
	}
}

// madeLabels returns label names an expression creates with label_replace,
// label_join or count_values.
func madeLabels(n *pqNode) []string {
	var out []string
	var walk func(*pqNode)
	walk = func(x *pqNode) {
		if x == nil {
			return
		}
		switch x.Kind {
		case pqCall, pqAgg:
			f := strings.ToLower(x.Func)
			switch {
			case (f == "label_replace" || f == "label_join") && len(x.Args) > 1 && x.Args[1].Kind == pqString:
				out = append(out, x.Args[1].Text)
			case f == "count_values" && len(x.Args) > 0 && x.Args[0].Kind == pqString:
				out = append(out, x.Args[0].Text)
			}
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

func sortedKeys(m map[string]string) []string {
	var ks []string
	for k := range m {
		ks = append(ks, k)
	}
	sort.Strings(ks)
	return ks
}

func tallyOf(m map[*rule]*tally, r *rule) *tally {
	t := m[r]
	if t == nil {
		t = &tally{}
		m[r] = t
	}
	return t
}

func matchVerb(n int) string {
	if n == 1 {
		return "matches"
	}
	return "match"
}

func joinQuoted(xs []string) string {
	if len(xs) == 1 {
		return xs[0]
	}
	if len(xs) == 2 {
		return xs[0] + " and " + xs[1]
	}
	return strings.Join(xs[:len(xs)-1], ", ") + " and " + xs[len(xs)-1]
}

func dedup(xs []string) []string {
	seen := map[string]bool{}
	var out []string
	for _, x := range xs {
		if !seen[x] {
			seen[x] = true
			out = append(out, x)
		}
	}
	return out
}

// seriesRead reports whether some selector reads the series' metric name.
// Labels are left out on purpose: tests often add a series with the right
// name and other labels to show the rule ignores it.
func seriesRead(s *inputSeries, rules []*rule, exprs []*exprTest) bool {
	name := s.Labels["__name__"]
	for _, r := range rules {
		for _, sel := range r.Node.selectors() {
			if sel.nameMatches(name) {
				return true
			}
		}
	}
	for _, e := range exprs {
		for _, sel := range e.Node.selectors() {
			if sel.nameMatches(name) {
				return true
			}
		}
	}
	return false
}

// suggest returns a defined alert name close to name, if there is one.
func suggest(name string, alerts map[string][]*rule) string {
	best, bestD := "", 4
	var names []string
	for n := range alerts {
		names = append(names, n)
	}
	sort.Strings(names)
	for _, n := range names {
		if d := editDistance(strings.ToLower(name), strings.ToLower(n)); d < bestD {
			best, bestD = n, d
		}
	}
	return best
}

func editDistance(a, b string) int {
	prev := make([]int, len(b)+1)
	cur := make([]int, len(b)+1)
	for j := range prev {
		prev[j] = j
	}
	for i := 1; i <= len(a); i++ {
		cur[0] = i
		for j := 1; j <= len(b); j++ {
			cost := 1
			if a[i-1] == b[j-1] {
				cost = 0
			}
			cur[j] = min3(prev[j]+1, cur[j-1]+1, prev[j-1]+cost)
		}
		prev, cur = cur, prev
	}
	return prev[len(b)]
}

func min3(a, b, c int) int {
	if b < a {
		a = b
	}
	if c < a {
		a = c
	}
	return a
}

// ---- can an expression return anything? ----

// miss is a selector that gets no sample in a test group.
type miss struct {
	Text string // the selector, as written
	Via  string // the recording rule that reads it, if any
	// Kind says why: "labels" when input series with that metric name
	// exist but none has the labels; "late" when matching series have no
	// sample until after the evaluation time; "nodata" when matching
	// series have only missing or stale samples; "absent" when no input
	// series has the metric name at all.
	Kind string
}

func (m miss) sel() string {
	if m.Via != "" {
		return fmt.Sprintf("%s (read by recording rule %s)", m.Text, m.Via)
	}
	return m.Text
}

type feedCtx struct {
	g        *testGroup
	records  []*rule
	visiting map[*rule]bool
}

// empty reports whether the expression is sure to return nothing at time t
// in this test group, because selectors it needs match no input series (and
// no recording rule makes them). It returns the selectors that matched
// nothing. It answers false whenever it isn't sure.
func (fc *feedCtx) empty(n *pqNode, t time.Duration) (bool, []miss) {
	switch n.Kind {
	case pqSelector:
		return fc.starved(n, t)
	case pqNumber, pqString:
		return false, nil
	case pqParen, pqUnary, pqSubquery:
		return fc.empty(n.Inner, t)
	case pqAgg:
		return fc.empty(n.Args[len(n.Args)-1], t)
	case pqCall:
		f := strings.ToLower(n.Func)
		if pqAlwaysOutput[f] || len(n.Args) == 0 {
			return false, nil
		}
		args := n.Args
		if f == "info" {
			// info returns its first argument unchanged when the info
			// metric is missing.
			args = args[:1]
		}
		var all []miss
		isEmpty := false
		for _, a := range args {
			if e, s := fc.empty(a, t); e {
				isEmpty = true
				all = append(all, s...)
			}
		}
		return isEmpty, all
	case pqBinary:
		le, ls := fc.empty(n.LHS, t)
		re, rs := fc.empty(n.RHS, t)
		switch {
		case n.Fill:
			// Fill modifiers make up the missing side, so the result is
			// empty only when both sides are.
			if le && re {
				return true, append(ls, rs...)
			}
			return false, nil
		case n.Op == "or":
			if le && re {
				return true, append(ls, rs...)
			}
			return false, nil
		case n.Op == "unless":
			return le, ls
		default:
			if le || re {
				var out []miss
				if le {
					out = append(out, ls...)
				}
				if re {
					out = append(out, rs...)
				}
				return true, out
			}
			return false, nil
		}
	}
	return false, nil
}

// starved reports whether the selector is sure to get no sample at or
// before t from the input series or a recording rule, and if so, what is
// missing: the selector itself, or the inputs of the recording rules that
// make it.
func (fc *feedCtx) starved(sel *pqNode, t time.Duration) (bool, []miss) {
	if sel.Metric == "ALERTS" || sel.Metric == "ALERTS_FOR_STATE" {
		return false, nil
	}
	late, nodata, otherLabels := false, false, false
	for _, s := range fc.g.Series {
		switch {
		case sel.matches(s.Labels) && !s.HasData:
			nodata = true
		case sel.matches(s.Labels):
			if s.First <= t || sel.Shifted {
				return false, nil
			}
			late = true
		case sel.nameMatches(s.Labels["__name__"]):
			otherLabels = true
		}
	}
	kind := "absent"
	switch {
	case late:
		kind = "late"
	case nodata:
		kind = "nodata"
	case otherLabels:
		kind = "labels"
	}
	var via []miss
	for _, r := range fc.records {
		if fc.visiting[r] || !sel.nameMatches(r.Record) {
			continue
		}
		fc.visiting[r] = true
		e, missing := fc.empty(r.Node, t)
		delete(fc.visiting, r)
		if !e {
			return false, nil
		}
		for _, m := range missing {
			if m.Via == "" {
				m.Via = r.Record
			}
			via = append(via, m)
		}
	}
	if len(via) > 0 {
		return true, via
	}
	return true, []miss{{Text: sel.Text, Kind: kind}}
}

// describeMisses explains the misses in words, and reports whether any of
// them is likely a mistake (wrong labels or late samples) rather than a
// metric left out on purpose.
func describeMisses(ms []miss, t time.Duration) (string, bool) {
	seen := map[string]bool{}
	var absent, mistakes []string
	for _, m := range ms {
		key := m.Kind + m.sel()
		if seen[key] {
			continue
		}
		seen[key] = true
		switch m.Kind {
		case "labels":
			mistakes = append(mistakes, fmt.Sprintf("no input series matches %s, though series with that metric name exist with other labels", m.sel()))
		case "late":
			mistakes = append(mistakes, fmt.Sprintf("the input series for %s have no sample until after %s", m.sel(), fmtDuration(t)))
		case "nodata":
			absent = append(absent, m.sel()+" (its input series have only _ or stale samples)")
		default:
			absent = append(absent, m.sel())
		}
	}
	if len(mistakes) > 0 {
		if len(absent) > 0 {
			mistakes = append(mistakes, fmt.Sprintf("no input series has the metric that %s %s", joinQuoted(absent), selVerb(len(absent))))
		}
		return strings.Join(mistakes, "; "), true
	}
	return fmt.Sprintf("no input series has the metric that %s %s", joinQuoted(absent), selVerb(len(absent))), false
}

func selVerb(n int) string {
	if n == 1 {
		return "selects"
	}
	return "select"
}
