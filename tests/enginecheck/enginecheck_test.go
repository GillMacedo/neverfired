// Package enginecheck checks neverfired's central claim against the
// Prometheus query engine: when neverfired says an alert's expression can't
// return anything in a test group (NF104 or NF107), the engine must agree.
//
// It takes every instant query in Prometheus's own engine tests
// (promql/promqltest/testdata), and for each one builds promtool test
// groups: one with all the loaded series, and one without each metric name
// in turn. neverfired reads them as an alert that must stay quiet; the
// engine evaluates the same expression on the same series.
//
// It is its own module because it needs the Prometheus source, which
// neverfired itself doesn't depend on. It needs network access the first
// time, to download that module.
package enginecheck

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/prometheus/prometheus/promql"
	"github.com/prometheus/prometheus/promql/promqltest"
)

type series struct {
	sel, values string
	name        string
}

type evalCase struct {
	file     string
	interval string
	series   []series
	expr     string
	at       string
}

func TestEngineAgrees(t *testing.T) {
	dir := prometheusDir(t)
	tool := buildNeverfired(t)
	cases := readCases(t, filepath.Join(dir, "promql", "promqltest", "testdata"))
	if len(cases) < 1000 {
		t.Fatalf("read only %d queries from the engine tests", len(cases))
	}
	eng := promqltest.NewTestEngineWithOpts(t, promql.EngineOpts{
		MaxSamples: 1e7, Timeout: time.Minute, LookbackDelta: 5 * time.Minute,
		EnableAtModifier: true, EnableNegativeOffset: true,
		NoStepSubqueryIntervalFn: func(int64) int64 { return time.Minute.Milliseconds() },
	})
	work := t.TempDir()
	var claims, checked, refused, engineEmptyOnly int
	for i, c := range cases {
		variants := variantsOf(c)
		rules := fmt.Sprintf("groups:\n  - name: probe\n    rules:\n      - alert: Probe\n        expr: %s\n", js(c.expr))
		var b strings.Builder
		b.WriteString("rule_files: [rules.yml]\nevaluation_interval: 1m\ntests:\n")
		lineOf := map[int]int{} // line of each alert_rule_test entry -> variant
		line := 4
		for vi, v := range variants {
			fmt.Fprintf(&b, "  - interval: %s\n    input_series:\n", c.interval)
			line++
			line++
			if len(v) == 0 {
				b.WriteString("      []\n")
				line++
			}
			for _, s := range v {
				fmt.Fprintf(&b, "      - series: %s\n        values: %s\n", js(s.sel), js(s.values))
				line += 2
			}
			fmt.Fprintf(&b, "    alert_rule_test:\n      - eval_time: %s\n        alertname: Probe\n        exp_alerts: []\n", c.at)
			line++
			lineOf[line] = vi
			line += 3
		}
		d := filepath.Join(work, fmt.Sprint(i))
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
		os.WriteFile(filepath.Join(d, "rules.yml"), []byte(rules), 0o644)
		os.WriteFile(filepath.Join(d, "t.yml"), []byte(b.String()), 0o644)
		cmd := exec.Command(tool, "--format", "json", "--fail-on", "never", "t.yml")
		cmd.Dir = d
		out, err := cmd.Output()
		if err != nil && os.Getenv("SHOW_REFUSED") != "" {
			if ee, ok := err.(*exec.ExitError); ok {
				t.Logf("refused %s: %s", c.expr, ee.Stderr)
			}
		}
		if err != nil {
			// Exit 2: neverfired refused the input (an expression its
			// parser doesn't accept). Nothing is claimed.
			refused++
			continue
		}
		var rep struct {
			Findings []struct {
				Code string
				Line int
			}
		}
		if err := json.Unmarshal(out, &rep); err != nil {
			t.Fatalf("%s: %v\n%s", c.file, err, out)
		}
		claimed := map[int]bool{}
		for _, f := range rep.Findings {
			if f.Code == "NF104" || f.Code == "NF107" {
				vi, ok := lineOf[f.Line]
				if !ok {
					t.Fatalf("%s: finding at line %d matches no test case:\n%s", c.file, f.Line, b.String())
				}
				claimed[vi] = true
			}
		}
		for vi, v := range variants {
			empty, ok := engineEmpty(t, eng, c, v)
			if !ok {
				continue
			}
			checked++
			if claimed[vi] {
				claims++
				if !empty {
					t.Errorf("%s: %s at %s: neverfired says the expression returns nothing with these series, the engine returns data:\n%v",
						c.file, c.expr, c.at, v)
				}
			} else if empty {
				engineEmptyOnly++
			}
		}
	}
	t.Logf("%d queries, %d variants checked against the engine, %d claimed empty by neverfired (all must be empty in the engine), %d empty in the engine but not claimed, %d queries refused by neverfired",
		len(cases), checked, claims, engineEmptyOnly, refused)
	if claims < 1000 {
		t.Errorf("only %d claims were checked; the test isn't exercising neverfired", claims)
	}
}

func js(s string) string {
	b, _ := json.Marshal(s)
	return string(b)
}

// variantsOf returns the series sets to try: all of them, then all but
// each metric name in turn.
func variantsOf(c evalCase) [][]series {
	out := [][]series{c.series}
	names := map[string]bool{}
	for _, s := range c.series {
		names[s.name] = true
	}
	var keys []string
	for k := range names {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, drop := range keys {
		var kept []series
		for _, s := range c.series {
			if s.name != drop {
				kept = append(kept, s)
			}
		}
		out = append(out, kept)
	}
	return out
}

func engineEmpty(t *testing.T, eng *promql.Engine, c evalCase, v []series) (empty, ok bool) {
	var load strings.Builder
	fmt.Fprintf(&load, "load %s\n", c.interval)
	for _, s := range v {
		fmt.Fprintf(&load, "  %s %s\n", s.sel, s.values)
	}
	t.Run("load", func(st *testing.T) {
		stor := promqltest.LoadedStorage(st, load.String())
		defer stor.Close()
		at, err := time.ParseDuration(promDur(c.at))
		if err != nil {
			return
		}
		q, err := eng.NewInstantQuery(context.Background(), stor, nil, c.expr, time.Unix(0, 0).Add(at))
		if err != nil {
			return
		}
		defer q.Close()
		res := q.Exec(context.Background())
		if res.Err != nil {
			return
		}
		switch r := res.Value.(type) {
		case promql.Vector:
			empty, ok = len(r) == 0, true
		case promql.Matrix:
			empty, ok = len(r) == 0, true
		}
	})
	return empty, ok
}

// promDur turns 1d and 1w into hours for time.ParseDuration.
func promDur(s string) string {
	var out strings.Builder
	num := ""
	for _, r := range s {
		switch {
		case r >= '0' && r <= '9':
			num += string(r)
		case r == 'd' || r == 'w' || r == 'y':
			n := 0
			fmt.Sscan(num, &n)
			mult := map[rune]int{'d': 24, 'w': 24 * 7, 'y': 24 * 365}[r]
			fmt.Fprintf(&out, "%dh", n*mult)
			num = ""
		default:
			out.WriteString(num + string(r))
			num = ""
		}
	}
	return out.String() + num
}

// readCases reads instant queries from the engine test scripts. It keeps
// queries whose series all come from plain load blocks with one interval,
// since a promtool test group has one interval.
func readCases(t *testing.T, dir string) []evalCase {
	files, err := filepath.Glob(filepath.Join(dir, "*.test"))
	if err != nil || len(files) == 0 {
		t.Fatalf("no engine tests in %s", dir)
	}
	var out []evalCase
	for _, f := range files {
		fh, err := os.Open(f)
		if err != nil {
			t.Fatal(err)
		}
		sc := bufio.NewScanner(fh)
		sc.Buffer(make([]byte, 1<<20), 1<<20)
		var lines []string
		for sc.Scan() {
			lines = append(lines, sc.Text())
		}
		fh.Close()
		indented := func(s string) bool {
			return (strings.HasPrefix(s, " ") || strings.HasPrefix(s, "\t")) && strings.TrimSpace(s) != ""
		}
		var cur []series
		interval := ""
		usable := true
		for i := 0; i < len(lines); i++ {
			l := strings.TrimSpace(lines[i])
			switch {
			case strings.HasPrefix(l, "clear"):
				cur, interval, usable = nil, "", true
			case strings.HasPrefix(l, "load"):
				fs := strings.Fields(l)
				if fs[0] != "load" || len(fs) != 2 || (interval != "" && fs[1] != interval) {
					usable = false
				}
				interval = fs[1]
				for i+1 < len(lines) && indented(lines[i+1]) {
					i++
					sl := strings.TrimSpace(lines[i])
					if strings.HasPrefix(sl, "#") {
						continue
					}
					if strings.Contains(sl, "@st") {
						usable = false
						continue
					}
					cut := strings.IndexAny(sl, " \t")
					if b := strings.Index(sl, "{"); b >= 0 && (cut < 0 || b < cut) {
						cut = strings.Index(sl, "}") + 1
					}
					sel := sl[:cut]
					name := sel
					if b := strings.Index(sel, "{"); b >= 0 {
						name = sel[:b]
					}
					if name == "" {
						usable = false
						continue
					}
					cur = append(cur, series{sel: sel, values: strings.TrimSpace(sl[cut:]), name: name})
				}
			case strings.HasPrefix(l, "eval") && strings.Contains(l, " instant at "):
				rest := l[strings.Index(l, " instant at ")+len(" instant at "):]
				sp := strings.IndexByte(rest, ' ')
				fail := strings.HasPrefix(l, "eval_fail")
				for i+1 < len(lines) && indented(lines[i+1]) {
					i++
					if strings.Contains(lines[i], "expect fail") {
						fail = true
					}
				}
				if sp < 0 || fail || !usable || interval == "" || len(cur) == 0 {
					continue
				}
				at := rest[:sp]
				if strings.ContainsAny(at, ".") {
					continue
				}
				out = append(out, evalCase{file: filepath.Base(f), interval: interval, series: append([]series(nil), cur...), expr: strings.TrimSpace(rest[sp:]), at: at})
			}
		}
	}
	return out
}

func prometheusDir(t *testing.T) string {
	out, err := exec.Command("go", "list", "-m", "-f", "{{.Dir}}", "github.com/prometheus/prometheus").Output()
	if err != nil {
		t.Fatalf("finding the Prometheus module: %v", err)
	}
	return strings.TrimSpace(string(out))
}

func buildNeverfired(t *testing.T) string {
	tool := filepath.Join(t.TempDir(), "neverfired")
	cmd := exec.Command("go", "build", "-o", tool, "./plugin/scripts")
	cmd.Dir = filepath.Join("..", "..")
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("building neverfired: %v\n%s", err, out)
	}
	return tool
}
