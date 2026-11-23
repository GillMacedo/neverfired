// Command neverfired reads promtool rule unit tests and the rule files they
// load, and reports tests that pass without checking anything and alerts
// that no test shows firing.
package main

import (
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

const version = "0.1.0"

type stringList []string

func (s *stringList) String() string     { return strings.Join(*s, ",") }
func (s *stringList) Set(v string) error { *s = append(*s, v); return nil }

func main() {
	os.Exit(run(os.Args[1:], os.Stdout, os.Stderr))
}

func usage(w io.Writer, fs *flag.FlagSet) {
	fmt.Fprintf(w, `Usage: neverfired [flags] TEST_FILE...

Reads promtool rule unit test files (the files you pass to "promtool test
rules") and the rule files they load, and reports tests that can't fail and
alerts no test shows firing.

Flags:
`)
	fs.PrintDefaults()
	fmt.Fprint(w, `
Exit codes: 0 nothing at or above --fail-on, 1 findings at or above
--fail-on, 2 the input can't be trusted or the flags are wrong.
`)
}

func run(args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("neverfired", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	var rules stringList
	fs.Var(&rules, "rules", "rule file or glob of `FILES` that should be covered by tests; repeat for more. Plain rule files and PrometheusRule objects")
	format := fs.String("format", "text", "output `format`: text or json")
	failOn := fs.String("fail-on", "error", "exit 1 when a finding is at or above this `level`: error, warn, note or never")
	ignoreUnknown := fs.Bool("ignore-unknown-fields", false, "ignore unknown fields in test and rule files, like promtool's flag of the same name")
	showVersion := fs.Bool("version", false, "print the version and exit")
	if err := fs.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			usage(stdout, fs)
			return 0
		}
		fmt.Fprintf(stderr, "neverfired: %v\n", err)
		usage(stderr, fs)
		return 2
	}
	if *showVersion {
		fmt.Fprintf(stdout, "neverfired %s\n", version)
		return 0
	}
	if *format != "text" && *format != "json" {
		fmt.Fprintf(stderr, "neverfired: --format must be text or json, not %q\n", *format)
		return 2
	}
	if _, ok := levelRank[*failOn]; !ok && *failOn != "never" {
		fmt.Fprintf(stderr, "neverfired: --fail-on must be error, warn, note or never, not %q\n", *failOn)
		return 2
	}
	if fs.NArg() == 0 {
		fmt.Fprintln(stderr, "neverfired: give at least one promtool test file")
		usage(stderr, fs)
		return 2
	}
	rep, err := load(fs.Args(), rules, *ignoreUnknown)
	if err != nil {
		fmt.Fprintf(stderr, "neverfired: nothing was checked, because the input can't be trusted: %v\n", err)
		return 2
	}
	if *format == "json" {
		enc := json.NewEncoder(stdout)
		enc.SetIndent("", "  ")
		if err := enc.Encode(rep); err != nil {
			fmt.Fprintf(stderr, "neverfired: %v\n", err)
			return 2
		}
	} else {
		printText(stdout, rep)
	}
	if *failOn == "never" {
		return 0
	}
	for _, f := range rep.Findings {
		if levelRank[f.Level] >= levelRank[*failOn] {
			return 1
		}
	}
	return 0
}

func load(testPaths, rulePatterns []string, ignoreUnknown bool) (*report, error) {
	cache := map[string]*ruleFile{}
	var tests []*testFile
	seen := map[string]bool{}
	for _, p := range testPaths {
		abs, _ := filepath.Abs(p)
		if seen[abs] {
			continue
		}
		seen[abs] = true
		if st, err := os.Stat(p); err == nil && st.IsDir() {
			return nil, refuse("%s is a directory; pass the test files themselves", p)
		}
		tf, err := loadTestFile(p, ignoreUnknown, cache)
		if err != nil {
			return nil, err
		}
		tests = append(tests, tf)
	}
	var extra []*ruleFile
	for _, pat := range rulePatterns {
		for _, part := range strings.Split(pat, ",") {
			part = strings.TrimSpace(part)
			if part == "" {
				continue
			}
			matches, err := filepath.Glob(part)
			if err != nil {
				return nil, refuse("--rules %q is not a valid glob: %v", part, err)
			}
			if len(matches) == 0 {
				return nil, refuse("--rules %q matches no file", part)
			}
			sort.Strings(matches)
			for _, m := range matches {
				if st, err := os.Stat(m); err == nil && st.IsDir() {
					return nil, refuse("--rules %s is a directory; pass files or a glob such as %s", m, filepath.Join(m, "*.yaml"))
				}
				rf, err := loadRuleFileCached(m, ignoreUnknown, cache, true)
				if err != nil {
					return nil, err
				}
				extra = append(extra, rf)
			}
		}
	}
	return analyze(tests, extra), nil
}

var statusText = map[string]string{
	"fires":      "fires in a test",
	"quiet-only": "only tested quiet",
	"untested":   "untested",
	"not-loaded": "no test loads it",
}

func printText(w io.Writer, rep *report) {
	for _, f := range rep.Findings {
		loc := f.File
		if f.Line > 0 {
			loc = fmt.Sprintf("%s:%d", f.File, f.Line)
		}
		fmt.Fprintf(w, "%s %s %s", strings.ToUpper(f.Level), f.Code, loc)
		if f.Where != "" {
			fmt.Fprintf(w, " [%s]", f.Where)
		}
		fmt.Fprintf(w, "\n  %s\n", f.Msg)
		if f.Fix != "" {
			fmt.Fprintf(w, "  fix: %s\n", f.Fix)
		}
	}
	if len(rep.Findings) > 0 {
		fmt.Fprintln(w)
	}
	counts := map[string]int{}
	if len(rep.Coverage) > 0 {
		width := len("alert")
		for _, c := range rep.Coverage {
			if len(c.Alert) > width {
				width = len(c.Alert)
			}
		}
		fmt.Fprintf(w, "%-*s  %-17s  %6s  %5s  %s\n", width, "alert", "status", "firing", "quiet", "rule")
		for _, c := range rep.Coverage {
			counts[c.Status]++
			quiet := fmt.Sprint(c.Quiet)
			if c.Vacuous > 0 {
				quiet = fmt.Sprintf("%d(%d)", c.Quiet, c.Vacuous)
			}
			loc := fmt.Sprintf("%s:%d", c.File, c.Line)
			if c.Via != "" {
				if c.Differs {
					loc += fmt.Sprintf(", tested as a different copy at %s", c.Via)
				} else {
					loc += fmt.Sprintf(", tested as %s", c.Via)
				}
			}
			fmt.Fprintf(w, "%-*s  %-17s  %6d  %5s  %s\n", width, c.Alert, statusText[c.Status], c.Firing, quiet, loc)
		}
		if anyVacuous(rep) {
			fmt.Fprintln(w, "(n) in the quiet column: tests expecting no alert that can't fail")
		}
		fmt.Fprintln(w)
	}
	lv := map[string]int{}
	for _, f := range rep.Findings {
		lv[f.Level]++
	}
	fmt.Fprintf(w, "neverfired: %s, %s, %s", plural(len(rep.TestFiles), "test file"), plural(len(rep.RuleFiles), "rule file"), plural(len(rep.Coverage), "alert"))
	if len(rep.Coverage) > 0 {
		var parts []string
		for _, s := range []string{"fires", "quiet-only", "untested", "not-loaded"} {
			if counts[s] > 0 {
				parts = append(parts, fmt.Sprintf("%d %s", counts[s], statusText[s]))
			}
		}
		fmt.Fprintf(w, " (%s)", strings.Join(parts, ", "))
	}
	if len(rep.Findings) == 0 {
		fmt.Fprintln(w, ": no problems found")
		return
	}
	fmt.Fprintf(w, ": %s, %s, %s\n", plural(lv["error"], "error"), plural(lv["warn"], "warning"), plural(lv["note"], "note"))
}

func anyVacuous(rep *report) bool {
	for _, c := range rep.Coverage {
		if c.Vacuous > 0 {
			return true
		}
	}
	return false
}

func plural(n int, s string) string {
	if n == 1 {
		return fmt.Sprintf("1 %s", s)
	}
	return fmt.Sprintf("%d %ss", n, s)
}
