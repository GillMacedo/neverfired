package main

import (
	"bytes"
	"strings"
	"testing"
	"time"
)

func TestParseSelectors(t *testing.T) {
	cases := map[string]string{
		`up`:                 `up`,
		`up{job="api"} == 0`: `up{job="api"}`,
		`sum by (job) (rate(x_total{code=~"5.."}[5m])) / sum by (job) (rate(x_total[5m]))`:                    `x_total{code=~"5.."} | x_total`,
		`histogram_quantile(0.99, sum by (le) (rate(h_bucket[5m]))) > 2`:                                      `h_bucket`,
		`a and on(job) b unless ignoring(x) group_left(y) c`:                                                  `a | b | c`,
		`count by (cluster) (kube_node_info) unless on (cluster) count by (cluster) (up{job="kubelet"} == 1)`: `kube_node_info | up{job="kubelet"}`,
		`changes(foo[24h:]) / 2 > 0`:                    `foo`,
		`max_over_time(foo[1h:5m] offset -10m) @ end()`: `foo`,
		`foo[5m] smoothed`:                              `foo`,
		`{__name__=~"job:.*", env!="dev"}`:              `{__name__=~"job:.*", env!="dev"}`,
		`{"metric.with.dots", "label.x"="1"}`:           `{"metric.with.dots", "label.x"="1"}`,
		`absent(up{job="x"})`:                           `up{job="x"}`,
		`-a ^ 2 * b`:                                    `a | b`,
		`a + fill(0) b`:                                 `a | b`,
		`h >/ 1.5`:                                      `h`,
		`foo[1m + step()]`:                              `foo`,
		`温度{場所="東京"}`:                                   `温度{場所="東京"}`,
		`sum(x) by (job)`:                               `x`,
		`topk(3, x) # a comment`:                        `x`,
		`time() - process_start_time_seconds > 600`:     `process_start_time_seconds`,
		`label_replace(up, "dst", "$1", "src", "(.*)")`: `up`,
	}
	for expr, want := range cases {
		n, err := parsePromQL(expr)
		if err != nil {
			t.Errorf("%s: %v", expr, err)
			continue
		}
		var got []string
		for _, s := range n.selectors() {
			got = append(got, s.Text)
		}
		if g := strings.Join(got, " | "); g != want {
			t.Errorf("%s: selectors %q, want %q", expr, g, want)
		}
	}
}

func TestParseRejects(t *testing.T) {
	for _, expr := range []string{
		``, `sum(`, `up{job="x"`, `up{job=x}`, `rat(foo[5m])`, `foo[5m`, `up ==`,
		`{job=~".*"}`, `up{job=~"("}`, `"unterminated`, `foo offset`, `a group_left b`,
	} {
		if _, err := parsePromQL(expr); err == nil {
			t.Errorf("%q: parsed, want an error", expr)
		}
	}
}

func TestMatchers(t *testing.T) {
	n, err := parsePromQL(`x{a="1", b!="2", c=~"3|4", d!~"5.*", e=""}`)
	if err != nil {
		t.Fatal(err)
	}
	sel := n.selectors()[0]
	match := map[string]string{"__name__": "x", "a": "1", "b": "9", "c": "4", "d": "6"}
	if !sel.matches(match) {
		t.Errorf("should match %v", match)
	}
	for k, v := range map[string]string{"a": "2", "b": "2", "c": "34", "d": "55", "e": "x", "__name__": "y"} {
		m := map[string]string{}
		for kk, vv := range match {
			m[kk] = vv
		}
		m[k] = v
		if sel.matches(m) {
			t.Errorf("should not match with %s=%q", k, v)
		}
	}
}

func TestDurations(t *testing.T) {
	good := map[string]time.Duration{"0": 0, "5m": 5 * time.Minute, "1h30m": 90 * time.Minute, "1d": 24 * time.Hour, "2w": 14 * 24 * time.Hour, "1500ms": 1500 * time.Millisecond, "1y": 365 * 24 * time.Hour}
	for s, want := range good {
		if got, err := parseDuration(s); err != nil || got != want {
			t.Errorf("%s: %v %v, want %v", s, got, err, want)
		}
	}
	for _, s := range []string{"", "5", "5 m", "1m1h", "1.5h", "-5m", "5min"} {
		if _, err := parseDuration(s); err == nil {
			t.Errorf("%q: parsed, want an error", s)
		}
	}
}

func TestFirstSample(t *testing.T) {
	cases := []struct {
		vals  string
		first int
		has   bool
	}{
		{"1 2 3", 0, true},
		{"_ _ 3", 2, true},
		{"_x3 1", 3, true},
		{"stale _ 0+1x10", 2, true},
		{"_x5", 0, false},
		{"stale stale", 0, false},
		{"-2+4x3", 0, true},
		{"1e3 -Inf NaN 0x1f", 0, true},
		{"_ {{schema:1 sum:3 count:2 buckets:[1 1]}}x4", 1, true},
		{"{{count:1}}+{{count:2}}x3", 0, true},
		{"", 0, false},
	}
	for _, c := range cases {
		first, has, err := firstSample(c.vals)
		if err != nil || first != c.first || has != c.has {
			t.Errorf("%q: %d %v %v, want %d %v", c.vals, first, has, err, c.first, c.has)
		}
	}
	for _, bad := range []string{"1+", "x3", "1 two", "{{count:1}", "1+2y3", "_x"} {
		if _, _, err := firstSample(bad); err == nil {
			t.Errorf("%q: read, want an error", bad)
		}
	}
}

// TestEmpty checks when an expression is sure to return nothing.
func TestEmpty(t *testing.T) {
	g := &testGroup{Series: []*inputSeries{
		{Labels: map[string]string{"__name__": "a", "job": "x"}, HasData: true},
		{Labels: map[string]string{"__name__": "b"}, HasData: true, First: 10 * time.Minute},
		{Labels: map[string]string{"__name__": "gone"}, HasData: false},
	}}
	recRule := func(name, expr string) *rule {
		n, err := parsePromQL(expr)
		if err != nil {
			t.Fatal(err)
		}
		return &rule{Record: name, Expr: expr, Node: n}
	}
	records := []*rule{recRule("job:a:sum", "sum by (job) (a)"), recRule("job:m:sum", "sum(missing)")}
	cases := map[string]bool{
		`a`:                             false,
		`a{job="y"}`:                    true,
		`missing`:                       true,
		`gone`:                          true,
		`b`:                             true, // first sample at 10m, evaluated at 5m
		`b offset -10m`:                 false,
		`a > 1`:                         false,
		`missing > 1`:                   true,
		`a / missing`:                   true,
		`a and missing`:                 true,
		`a or missing`:                  false,
		`missing or missing2`:           true,
		`a unless missing`:              false,
		`missing unless a`:              true,
		`absent(missing)`:               false,
		`absent_over_time(missing[5m])`: false,
		`vector(1)`:                     false,
		`time() > 5`:                    false,
		`sum(missing)`:                  true,
		`count(missing) == 0`:           true,
		`count(missing) or vector(0)`:   false,
		`rate(missing[5m])`:             true,
		`histogram_quantile(0.9, rate(missing[5m]))`: true,
		`scalar(missing)`:       false,
		`a + fill(0) missing`:   false,
		`job:a:sum`:             false,
		`job:m:sum`:             true,
		`ALERTS{alertname="X"}`: false,
		`label_replace(missing, "a", "b", "c", "d")`: true,
		`info(a, {k="v"})`:                           false,
		`-missing`:                                   true,
		`(missing)[10m:1m]`:                          true,
	}
	for expr, want := range cases {
		n, err := parsePromQL(expr)
		if err != nil {
			t.Errorf("%s: %v", expr, err)
			continue
		}
		fc := &feedCtx{g: g, records: records, visiting: map[*rule]bool{}}
		if got, _ := fc.empty(n, 5*time.Minute); got != want {
			t.Errorf("%s: empty = %v, want %v", expr, got, want)
		}
	}
}

func TestMissKinds(t *testing.T) {
	g := &testGroup{Series: []*inputSeries{
		{Labels: map[string]string{"__name__": "req", "status": "500"}, HasData: true},
		{Labels: map[string]string{"__name__": "late"}, HasData: true, First: time.Hour},
	}}
	fc := &feedCtx{g: g, visiting: map[*rule]bool{}}
	for expr, kind := range map[string]string{`req{code="500"}`: "labels", `late`: "late", `other`: "absent"} {
		n, _ := parsePromQL(expr)
		_, ms := fc.empty(n, time.Minute)
		if len(ms) != 1 || ms[0].Kind != kind {
			t.Errorf("%s: %+v, want kind %s", expr, ms, kind)
		}
	}
}

func TestRunRefusesWithoutOutput(t *testing.T) {
	var out, errb bytes.Buffer
	if code := run([]string{"/does/not/exist.yml"}, &out, &errb); code != 2 || out.Len() != 0 {
		t.Errorf("exit %d, stdout %q", code, out.String())
	}
	if code := run([]string{"--version"}, &out, &errb); code != 0 || !strings.Contains(out.String(), version) {
		t.Errorf("--version: exit %d, %q", code, out.String())
	}
}

func TestNoDataAndAliases(t *testing.T) {
	g := &testGroup{Series: []*inputSeries{
		{Labels: map[string]string{"__name__": "blank", "job": "x"}, HasData: false},
	}}
	fc := &feedCtx{g: g, visiting: map[*rule]bool{}}
	n, _ := parsePromQL(`blank{job="x"}`)
	if e, ms := fc.empty(n, time.Hour); !e || len(ms) != 1 || ms[0].Kind != "nodata" {
		t.Errorf("all-missing series: %v %+v", e, ms)
	}
	n, _ = parsePromQL(`blank{job="y"}`)
	if _, ms := fc.empty(n, time.Hour); len(ms) != 1 || ms[0].Kind != "labels" {
		t.Errorf("other labels: %+v", ms)
	}
	data := []byte("rule_files: [r.yml]\ntests:\n  - interval: 1m\n    input_series: &in\n      - series: 'up'\n        values: '1x5'\n    alert_rule_test:\n      - eval_time: 1m\n        alertname: &name A\n        exp_alerts: []\n  - input_series: *in\n    alert_rule_test:\n      - {eval_time: 2m, alertname: *name}\n")
	root, err := parseYAML(data, "t.yml", "test file")
	if err != nil {
		t.Fatal(err)
	}
	c := &yamlCtx{path: "t.yml"}
	top, err := c.fields(root, "the test file", "rule_files", "tests")
	if err != nil {
		t.Fatal(err)
	}
	groups, _ := c.list(top["tests"], "tests")
	for i, gn := range groups {
		g, err := loadGroup(c, gn, i, time.Minute)
		if err != nil || len(g.Series) != 1 || g.Alerts[0].Name != "A" {
			t.Errorf("group %d with aliases: %v %+v", i, err, g)
		}
	}
}
