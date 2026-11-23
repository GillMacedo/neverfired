package tests

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"
)

func TestCheckoutExample(t *testing.T) {
	r := neverfired(t, "examples/checkout/alerts_test.yml")
	want := []string{"ERROR NF101", "ERROR NF102", "ERROR NF104", "WARN NF103", "WARN NF201", "WARN NF201", "WARN NF202"}
	if got := codes(r.stdout); !reflect.DeepEqual(got, want) {
		t.Errorf("codes = %v, want %v\n%s", got, want, r.stdout)
	}
	if r.code != 1 {
		t.Errorf("exit %d, want 1 (errors, default --fail-on error)", r.code)
	}
	for _, s := range []string{
		`checkout_requests_total{code=~"5.."} (read by recording rule job:checkout_requests_errors:ratio_rate5m)`,
		"did you mean CheckoutDown?",
		`rule_files entry "shared/*.yml" matches no file`,
	} {
		if !strings.Contains(r.stdout, s) {
			t.Errorf("output lacks %q", s)
		}
	}
}

func TestHealthyExampleStaysQuiet(t *testing.T) {
	for _, failOn := range []string{"error", "warn", "note"} {
		r := neverfired(t, "--fail-on", failOn, "examples/healthy/alerts_test.yml")
		if r.code != 0 || len(codes(r.stdout)) != 0 || !strings.Contains(r.stdout, "no problems found") {
			t.Errorf("--fail-on %s: exit %d\n%s%s", failOn, r.code, r.stdout, r.stderr)
		}
	}
}

func TestFailOn(t *testing.T) {
	cases := []struct {
		args []string
		code int
	}{
		{[]string{"examples/checkout/alerts_test.yml"}, 1},
		{[]string{"--fail-on", "error", "examples/checkout/alerts_test.yml"}, 1},
		{[]string{"--fail-on", "never", "examples/checkout/alerts_test.yml"}, 0},
		{[]string{"--fail-on", "warn", "examples/healthy/alerts_test.yml"}, 0},
		{[]string{"--fail-on", "bogus", "examples/healthy/alerts_test.yml"}, 2},
		{[]string{"--format", "xml", "examples/healthy/alerts_test.yml"}, 2},
		{[]string{}, 2},
		{[]string{"--no-such-flag", "examples/healthy/alerts_test.yml"}, 2},
	}
	for _, c := range cases {
		if r := neverfired(t, c.args...); r.code != c.code {
			t.Errorf("%v: exit %d, want %d\n%s%s", c.args, r.code, c.code, r.stdout, r.stderr)
		}
	}
	// Only warnings: exit 0 by default, 1 with --fail-on warn.
	dir := writeFiles(t, map[string][]byte{
		"rules.yml": readFile(t, "examples/healthy/alerts.yml"),
		"t.yml": []byte(`rule_files: [rules.yml]
tests:
  - interval: 1m
    input_series:
      - series: 'up{job="checkout"}'
        values: '0x20'
    alert_rule_test:
      - eval_time: 10m
        alertname: CheckoutDown
        exp_alerts:
          - exp_labels: {job: checkout, severity: page}
`),
	})
	if r := neverfiredIn(t, dir, "t.yml"); r.code != 0 || !reflect.DeepEqual(codes(r.stdout), []string{"WARN NF201"}) {
		t.Errorf("warnings only: exit %d\n%s", r.code, r.stdout)
	}
	if r := neverfiredIn(t, dir, "--fail-on", "warn", "t.yml"); r.code != 1 {
		t.Errorf("warnings only with --fail-on warn: exit %d", r.code)
	}
}

func TestRulesFlagFindsDriftAndUnloadedAlerts(t *testing.T) {
	r := neverfired(t, "--fail-on", "never", "--rules", "examples/checkout/deploy/*.yaml", "examples/checkout/alerts_test.yml")
	got := codes(r.stdout)
	for _, c := range []string{"WARN NF203", "WARN NF204"} {
		found := false
		for _, g := range got {
			found = found || g == c
		}
		if !found {
			t.Errorf("missing %s in %v\n%s", c, got, r.stdout)
		}
	}
	if !strings.Contains(r.stdout, `has expr "job:checkout_requests_errors:ratio_rate5m > 0.05" for 10m, and this file has expr "job:checkout_requests_errors:ratio_rate5m > 0.02" for 10m`) {
		t.Errorf("NF204 doesn't show both expressions:\n%s", r.stdout)
	}
	// The deployed copies of tested alerts count as tested.
	if !strings.Contains(r.stdout, "tested as examples/checkout/alerts.yml:18") {
		t.Errorf("CheckoutDown's deployed copy isn't matched to the tested one:\n%s", r.stdout)
	}
}

func TestJSON(t *testing.T) {
	r := neverfired(t, "--format", "json", "--fail-on", "never", "examples/checkout/alerts_test.yml")
	var rep struct {
		TestFiles []string `json:"test_files"`
		Findings  []struct {
			Code, Level, File string
			Line              int
		} `json:"findings"`
		Coverage []struct {
			Alert   string
			Status  string
			Vacuous int `json:"quiet_tests_that_cannot_fail"`
		} `json:"coverage"`
	}
	if err := json.Unmarshal([]byte(r.stdout), &rep); err != nil {
		t.Fatalf("%v\n%s", err, r.stdout)
	}
	if len(rep.Findings) != 7 || len(rep.Coverage) != 4 || rep.Coverage[0].Alert != "CheckoutHighErrorRate" || rep.Coverage[0].Vacuous != 1 {
		t.Errorf("unexpected JSON: %+v", rep)
	}
	for _, f := range rep.Findings {
		if f.Line == 0 || f.File == "" {
			t.Errorf("finding without a position: %+v", f)
		}
	}
	h := neverfired(t, "--format", "json", "examples/healthy/alerts_test.yml")
	if h.code != 0 || !strings.Contains(h.stdout, `"findings": []`) {
		t.Errorf("healthy JSON: exit %d\n%s", h.code, h.stdout)
	}
}

// A test file without rule_files is valid; its alert tests name alerts
// that don't exist.
func TestNoRuleFiles(t *testing.T) {
	dir := writeFiles(t, map[string][]byte{"t.yml": []byte("tests:\n  - input_series: []\n    alert_rule_test:\n      - {eval_time: 1m, alertname: X}\n")})
	r := neverfiredIn(t, dir, "t.yml")
	if r.code != 1 || !reflect.DeepEqual(codes(r.stdout), []string{"ERROR NF102"}) {
		t.Errorf("exit %d\n%s%s", r.code, r.stdout, r.stderr)
	}
}
