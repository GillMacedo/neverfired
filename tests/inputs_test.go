package tests

import (
	"strings"
	"testing"
)

// badInputs are files neverfired must refuse, whichever input they stand in
// for: empty, garbage, cut off, or the output of a failed command.
func badInputs(t *testing.T, real []byte) map[string][]byte {
	half := real[:len(real)/2]
	// Cut inside a quoted string, without a final newline.
	quote := strings.Index(string(real), "'")
	if quote < 0 {
		quote = strings.Index(string(real), `"`)
	}
	return map[string][]byte{
		"empty":            {},
		"white space":      []byte("  \n\t\n\n"),
		"comments only":    []byte("# rules go here\n# later\n"),
		"binary":           {0x7f, 'E', 'L', 'F', 2, 1, 1, 0, 0, 0, '\n'},
		"invalid UTF-8":    []byte("groups: \xff\xfe\n"),
		"plain text":       []byte("hello world\n"),
		"a YAML list":      []byte("- a\n- b\n"),
		"a JSON object":    []byte(`{"name": "x", "items": [1, 2]}` + "\n"),
		"cut in half":      half,
		"cut in a string":  real[:quote+3],
		"no final newline": real[:len(real)-1],
		"promtool test output": []byte(`Processing test file: alerts_test.yml
  FAILED:
    alertname: CheckoutDown, time: 12m,
        exp:[
            0:
              Labels:{alertname="CheckoutDown", instance="checkout-0", job="checkout", severity="page"}
            ],
        got:[]

`),
		"promtool check output": []byte("Checking alerts.yml\n  FAILED:\nalerts.yml: 5:11: group \"checkout\", rule 2, \"CheckoutDown\": could not parse expression: 1:20: parse error: unexpected <EOF>\n"),
		"go build output":       []byte("# github.com/acme/rules\n./gen.go:12:2: undefined: render\n"),
		"jsonnet error":         []byte("RUNTIME ERROR: field does not exist: alerts\n\tmixin.libsonnet:12:5-20\tobject <anonymous>\n\tDuring manifestation\n"),
		"helm error":            []byte("Error: template: monitoring/templates/rules.yaml:3:14: executing \"monitoring/templates/rules.yaml\" at <.Values.rules>: nil pointer evaluating interface {}.rules\n"),
		"kubectl error":         []byte("error: the server doesn't have a resource type \"prometheusrules\"\n"),
		"make error":            []byte("make: *** [Makefile:12: rules.yml] Error 1\n"),
		"panic":                 []byte("panic: runtime error: index out of range [3] with length 3\n\ngoroutine 1 [running]:\nmain.main()\n"),
		"two documents":         append(append([]byte{}, real...), append([]byte("---\n"), real...)...),
	}
}

// TestBadTestFile replaces the test file and keeps the real rule file.
func TestBadTestFile(t *testing.T) {
	rules := readFile(t, "examples/healthy/alerts.yml")
	real := readFile(t, "examples/healthy/alerts_test.yml")
	cases := badInputs(t, real)
	cases["rule file as test file"] = rules
	cases["no tests"] = []byte("rule_files: [alerts.yml]\ntests: []\n")
	cases["group without test cases"] = []byte("rule_files: [alerts.yml]\ntests:\n  - interval: 1m\n    input_series:\n      - series: 'up{job=\"checkout\"}'\n        values: '1x10'\n")
	cases["series without values"] = []byte("rule_files: [alerts.yml]\ntests:\n  - input_series:\n      - series: 'up'\n    alert_rule_test:\n      - {eval_time: 1m, alertname: CheckoutDown}\n")
	cases["alert test without alertname"] = []byte("rule_files: [alerts.yml]\ntests:\n  - input_series: []\n    alert_rule_test:\n      - eval_time: 1m\n")
	cases["misspelled field"] = []byte(strings.Replace(string(real), "alert_rule_test", "alert_rule_tests", 1))
	cases["bad duration"] = []byte(strings.Replace(string(real), "eval_time: 20m", "eval_time: 20 minutes", 1))
	cases["bad values"] = []byte(strings.Replace(string(real), "'0+90x30'", "'0+90y30'", 1))
	cases["bad series"] = []byte(strings.Replace(string(real), `job="checkout", code="200"`, `job="checkout", code=~"200"`, 1))
	cases["bad expr"] = []byte("rule_files: [alerts.yml]\ntests:\n  - input_series: []\n    promql_expr_test:\n      - {expr: 'sum(rate(x[5m])', eval_time: 1m}\n")
	for name, data := range cases {
		dir := writeFiles(t, map[string][]byte{"alerts.yml": rules, "alerts_test.yml": data})
		for _, failOn := range []string{"error", "never"} {
			for _, format := range []string{"text", "json"} {
				refused(t, "test file "+name+" --fail-on "+failOn+" --format "+format,
					neverfiredIn(t, dir, "--fail-on", failOn, "--format", format, "alerts_test.yml"))
			}
		}
	}
	dir := writeFiles(t, map[string][]byte{"alerts.yml": rules})
	refused(t, "missing test file", neverfiredIn(t, dir, "alerts_test.yml"))
	refused(t, "directory as test file", neverfiredIn(t, dir, "."))
}

// TestBadRuleFile replaces the rule file the real test file loads.
func TestBadRuleFile(t *testing.T) {
	rules := readFile(t, "examples/healthy/alerts.yml")
	tests := readFile(t, "examples/healthy/alerts_test.yml")
	cases := badInputs(t, rules)
	cases["test file as rule file"] = tests
	cases["no groups"] = []byte("groups: []\n")
	cases["group without rules"] = []byte("groups:\n  - name: checkout\n    rules: []\n")
	cases["rule without expr"] = []byte("groups:\n  - name: checkout\n    rules:\n      - alert: CheckoutDown\n        for: 5m\n")
	cases["rule with alert and record"] = []byte("groups:\n  - name: c\n    rules:\n      - alert: A\n        record: b\n        expr: up\n")
	cases["unparsable expr"] = []byte(strings.Replace(string(rules), `up{job="checkout"} == 0`, `up{job="checkout" == 0`, 1))
	cases["unknown function"] = []byte(strings.Replace(string(rules), `rate(checkout_requests_total[5m])`, `rat(checkout_requests_total[5m])`, 1))
	cases["misspelled field"] = []byte(strings.Replace(string(rules), "expr: up", "exp: up", 1))
	cases["PrometheusRule in rule_files"] = readFile(t, "examples/checkout/deploy/prometheusrule.yaml")
	for name, data := range cases {
		dir := writeFiles(t, map[string][]byte{"alerts.yml": data, "alerts_test.yml": tests})
		for _, failOn := range []string{"error", "never"} {
			for _, format := range []string{"text", "json"} {
				refused(t, "rule file "+name+" --fail-on "+failOn+" --format "+format,
					neverfiredIn(t, dir, "--fail-on", failOn, "--format", format, "alerts_test.yml"))
			}
		}
	}
}

// TestBadRulesFlag passes a bad file to --rules next to a real test file.
func TestBadRulesFlag(t *testing.T) {
	rules := readFile(t, "examples/healthy/alerts.yml")
	tests := readFile(t, "examples/healthy/alerts_test.yml")
	crd := readFile(t, "examples/checkout/deploy/prometheusrule.yaml")
	cases := badInputs(t, crd)
	cases["ConfigMap"] = []byte("apiVersion: v1\nkind: ConfigMap\nmetadata: {name: x}\ndata: {a: b}\n")
	cases["PrometheusRule without spec"] = []byte("apiVersion: monitoring.coreos.com/v1\nkind: PrometheusRule\nmetadata: {name: x}\n")
	delete(cases, "two documents") // two PrometheusRules in one file are fine
	for name, data := range cases {
		dir := writeFiles(t, map[string][]byte{"alerts.yml": rules, "alerts_test.yml": tests, "deploy/rules.yaml": data})
		for _, failOn := range []string{"error", "never"} {
			refused(t, "--rules "+name+" --fail-on "+failOn,
				neverfiredIn(t, dir, "--fail-on", failOn, "--rules", "deploy/rules.yaml", "alerts_test.yml"))
		}
	}
	dir := writeFiles(t, map[string][]byte{"alerts.yml": rules, "alerts_test.yml": tests})
	refused(t, "--rules matching nothing", neverfiredIn(t, dir, "--rules", "deploy/*.yaml", "alerts_test.yml"))
	refused(t, "--rules directory", neverfiredIn(t, dir, "--rules", ".", "alerts_test.yml"))
	two := writeFiles(t, map[string][]byte{"alerts.yml": rules, "alerts_test.yml": tests, "deploy/rules.yaml": append(append([]byte{}, crd...), append([]byte("---\n"), crd...)...)})
	if r := neverfiredIn(t, two, "--fail-on", "never", "--rules", "deploy/rules.yaml", "alerts_test.yml"); r.code != 0 {
		t.Errorf("two PrometheusRules in one file: exit %d\n%s", r.code, r.stderr)
	}
}

// TestEveryPrefix cuts each real input at every byte. A cut that doesn't end
// a line must be refused. A cut at the end of a line can be a complete,
// shorter file; it must still never be reported clean, since the healthy
// suite needs every line to have no findings.
func TestEveryPrefix(t *testing.T) {
	rules := readFile(t, "examples/healthy/alerts.yml")
	tests := readFile(t, "examples/healthy/alerts_test.yml")
	for _, side := range []string{"alerts_test.yml", "alerts.yml"} {
		full := tests
		if side == "alerts.yml" {
			full = rules
		}
		for n := 0; n < len(full); n++ {
			files := map[string][]byte{"alerts.yml": rules, "alerts_test.yml": tests}
			files[side] = full[:n]
			dir := writeFiles(t, files)
			r := neverfiredIn(t, dir, "--fail-on", "warn", "alerts_test.yml")
			if n == 0 || full[n-1] != '\n' {
				refused(t, side+" cut at byte "+itoa(n), r)
				continue
			}
			if r.code == 0 || strings.Contains(r.stdout, "no problems found") {
				t.Errorf("%s cut after byte %d (a line end) was reported clean:\n%s", side, n, r.stdout)
			}
		}
	}
}

func itoa(n int) string { return fmtInt(n) }

func fmtInt(n int) string {
	if n == 0 {
		return "0"
	}
	var b []byte
	for n > 0 {
		b = append([]byte{byte('0' + n%10)}, b...)
		n /= 10
	}
	return string(b)
}
