---
name: checking-promtool-alert-tests
description: Checks Prometheus alerting rule unit tests (the files run with promtool test rules) and the rule files they load. Reports alerts that no test shows firing, alerts no test names, tests that pass whatever the rules say (an alertname that doesn't exist, a rule_files glob that matches nothing, a "no alert" check whose input series the expression never reads because of wrong labels or late samples, a check before the alert's for duration), expected labels or annotations promtool can't match, and rules in PrometheusRule objects or other rule files that no test loads or that differ from the tested copy. Use when asked whether Prometheus alerts are tested, to review a change to alert rules or their promtool tests, when promtool test rules passes but an alert never fired in production, or to measure alert test coverage for monitoring mixins, Helm charts or kube-prometheus rules.
---

# Checking promtool alert tests

neverfired reads promtool test files (`rule_files`, `tests`, `input_series`, `alert_rule_test`, `promql_expr_test`) and the rule files they load, parses each rule's PromQL, and works out which tests can't fail and which alerts no test shows firing. It doesn't run promtool or Prometheus and needs no network. It needs Go 1.22 or newer.

## Find the inputs

1. **Test files:** the files the project passes to `promtool test rules`. Look in its Makefile, CI workflow or `tests/` directory.
2. **Generated rules:** if `rule_files` point at generated files (jsonnet mixins, Helm), generate them first with the project's own command, and check it exited 0. A rule file that is missing makes NF101; one that is empty or cut off is refused.
3. **Deployed rules (optional):** PrometheusRule manifests or rule files that should be covered, passed with `--rules`. Quote globs.

## Run it

Build it once, then run it:

    go build -o "${TMPDIR:-/tmp}/neverfired" $(ls "${CLAUDE_PLUGIN_ROOT}"/scripts/*.go | grep -v _test.go)
    "${TMPDIR:-/tmp}/neverfired" tests/*.yml
    "${TMPDIR:-/tmp}/neverfired" --rules 'deploy/*.yaml' tests/alerts_test.yml

Add `--format json` for machine-readable output, `--fail-on warn` to fail on coverage gaps too, and `--ignore-unknown-fields` if the project runs promtool with that flag.

## Read the exit code first

- **0:** nothing at or above `--fail-on` (default `error`). Warnings and the coverage table may still matter; read them.
- **1:** findings at or above `--fail-on`.
- **2:** nothing was checked. Stderr says why: a file that is missing, empty, binary, not YAML, cut off (no final newline, or a test case missing a field), the output of a failed command, a misspelled field, an expression that doesn't parse, or a test file with no tests. Fix the input and run again. Never describe exit 2 as a clean result.

## Check each finding

- **Tests that can't fail (errors):** NF101 a `rule_files` entry that matches no file (promtool only warns); NF102 an alertname no loaded rule defines; NF104 a "no alert" check where the expression gets no data because series have the wrong labels or start too late; NF105 the same for a `promql_expr_test` that expects nothing; NF106 expected labels or annotations the rule can't produce, so promtool fails the test.
- **Weak tests (warnings):** NF103 a "no alert" check before the alert's `for` has elapsed, with no later check of that alert in the group; NF107 and NF108 a "no alert" or "no samples" check where a metric the expression needs is missing from the input entirely. NF107 and NF108 are fine when the test is about that metric being absent; say so.
- **Coverage (warnings):** NF201 an alert no test names; NF202 an alert tested only as quiet; NF203 an alert in a `--rules` file that no test loads; NF204 a deployed alert whose tested copy has a different expression or `for`.
- **Note:** NF301 an input series whose metric name nothing reads, often a typo.

Open the test file at each line before you report it. For NF104, show the series and the selector side by side. For NF107, read the test's name and comments to judge whether the missing metric is deliberate.

## Report

1. Errors first, with the file and line and the smallest change to the test that makes it meaningful.
2. Then the coverage table's untested and quiet-only alerts, most severe labels first (for example `severity: page`).
3. Say what wasn't checked: neverfired doesn't evaluate PromQL, so a passing positive test is promtool's job; run `promtool test rules` too.

## Don't

Don't say the alerts are well tested because neverfired found nothing. It shows each alert has a test that expects it to fire and that quiet checks get data; it doesn't judge thresholds, label sets in production, or whether the scenarios are realistic. Don't edit rule files to make findings go away.
