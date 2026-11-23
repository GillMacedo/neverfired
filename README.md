# neverfired: find Prometheus alerts your promtool tests never show firing

[![CI](https://github.com/GillMacedo/neverfired/actions/workflows/ci.yml/badge.svg)](https://github.com/GillMacedo/neverfired/actions/workflows/ci.yml) [![MIT license](https://img.shields.io/badge/license-MIT-blue)](LICENSE)

**A Claude Code skill and Go CLI for teams that unit test Prometheus alerting rules with `promtool test rules`. It reads the test files and the rule files they load, and reports the alerts no test shows firing and the tests that pass without checking anything: an `alertname` with a typo, a `rule_files` glob that matches no file, a "no alert" check whose input series the expression never reads, and a "no alert" check that runs before the alert's `for` has elapsed.**

`promtool test rules` prints SUCCESS for all of these. A test that expects no alert passes when the alert name is misspelled, because a rule that doesn't exist never fires. It passes when the input series have a `status` label and the rule reads `code`, because the expression gets no data. It passes at `eval_time: 3m` for an alert with `for: 5m`, whatever the input. And when a `rule_files` pattern matches nothing, promtool prints a warning and still reports SUCCESS. These tests stay green while the rule they were written for changes or breaks.

neverfired parses each rule's PromQL and each test's input series, and works out, test group by test group, whether the expression can return anything at the test's `eval_time`. It claims that an expression returns nothing only when that holds for every possible sample value, and that claim is checked against the Prometheus query engine on 1,688 queries from Prometheus's own engine tests. It also lists every alert with the number of tests that expect it to fire, and with `--rules` it compares the rules you deploy, such as PrometheusRule objects, with the copies your tests load.

## What you can ask Claude

- "Which of our Prometheus alerts have no promtool test that makes them fire?"
- "Our promtool tests pass, but HighErrorRate never fired in the last incident. Are the tests checking anything?"
- "Review this PR that changes alerts.yml and its promtool tests."
- "Do the alerts in our PrometheusRule manifests match the rule files our tests load?"
- "Measure alert test coverage for this monitoring mixin."

## Install

As a Claude Code plugin:

    claude plugin marketplace add GillMacedo/neverfired
    claude plugin install neverfired@neverfired-marketplace

Or run it with Go 1.22 or newer. It has no dependencies outside the standard library:

    go run github.com/GillMacedo/neverfired/plugin/scripts@v0.1.0 tests/*.yml

## Example

`examples/checkout` is a small suite for a made-up checkout service. `promtool test rules` reports SUCCESS for it (with a warning about `shared/*.yml`). neverfired finds three tests that can't fail and three alerts that no test makes fire. `--fail-on never` keeps the exit code at 0 for this page; without it, the run exits 1:

    $ go run ./plugin/scripts --fail-on never examples/checkout/alerts_test.yml
    ERROR NF101 examples/checkout/alerts_test.yml:3 [rule_files]
      rule_files entry "shared/*.yml" matches no file (looked in examples/checkout); promtool only prints a warning and still reports SUCCESS
      fix: fix the path; rule_files are relative to the test file's directory
    ERROR NF104 examples/checkout/alerts_test.yml:35 [test group "healthy traffic doesn't page", CheckoutHighErrorRate at 20m]
      this test expects no alert, but the alert's expression can't return anything at 20m: no input series matches checkout_requests_total{code=~"5.."} (read by recording rule job:checkout_requests_errors:ratio_rate5m), though series with that metric name exist with other labels; the test passes whatever values the series have
      fix: fix the series' labels or sample times so the expression reads them, with values that stay under the threshold
    ERROR NF102 examples/checkout/alerts_test.yml:48 [test group "checkout up", CheckoutDwn at 20m]
      no alert named CheckoutDwn is defined in the rule files this test loads, so this test expects no alert from a rule that doesn't exist and passes whatever the rules do (did you mean CheckoutDown?)
      fix: correct the alertname, or add the rule file that defines it to rule_files
    WARN NF202 examples/checkout/alerts.yml:18 [alert CheckoutDown]
      no test expects this alert to fire; 1 test case expects it to stay quiet, and 1 of them can't fail (see above), so nothing shows the expression ever matches
      fix: add an alert_rule_test case with exp_alerts that makes the alert fire
    WARN NF201 examples/checkout/alerts.yml:24 [alert CheckoutSlow]
      no test names this alert, though examples/checkout/alerts_test.yml loads its file
      fix: add an alert_rule_test case whose input makes the alert fire, with its expected labels
    WARN NF201 examples/checkout/alerts.yml:33 [alert PaymentQueueStuck]
      no test names this alert, though examples/checkout/alerts_test.yml loads its file
      fix: add an alert_rule_test case whose input makes the alert fire, with its expected labels
    WARN NF103 examples/checkout/alerts_test.yml:45 [test group "checkout up", CheckoutDown at 3m]
      this test expects no alert at 3m, but the alert has for: 5m, so it can't fire before 5m whatever the input; the test checks only the for duration, never the expression, and this group has no later check of the alert
      fix: move eval_time to 5m or later, after the input has stayed in the state you want to show doesn't alert

    alert                  status             firing  quiet  rule
    CheckoutHighErrorRate  fires in a test         1   1(1)  examples/checkout/alerts.yml:10
    CheckoutDown           only tested quiet       0   1(1)  examples/checkout/alerts.yml:18
    CheckoutSlow           untested                0      0  examples/checkout/alerts.yml:24
    PaymentQueueStuck      untested                0      0  examples/checkout/alerts.yml:33
    (n) in the quiet column: tests expecting no alert that can't fail

    neverfired: 1 test file, 1 rule file, 4 alerts (1 fires in a test, 1 only tested quiet, 2 untested): 3 errors, 4 warnings, 0 notes

The healthy suite in `examples/healthy` tests the same rules properly and stays quiet:

    $ go run ./plugin/scripts examples/healthy/alerts_test.yml
    alert                  status             firing  quiet  rule
    CheckoutHighErrorRate  fires in a test         1      1  examples/healthy/alerts.yml:10
    CheckoutDown           fires in a test         1      1  examples/healthy/alerts.yml:18

    neverfired: 1 test file, 1 rule file, 2 alerts (2 fires in a test): no problems found

With `--rules`, the deployed PrometheusRule is compared with the tested rule file. Its error threshold is 0.02 where the tests check 0.05, and it has an alert no test loads:

    $ go run ./plugin/scripts --fail-on never --rules 'examples/checkout/deploy/*.yaml' examples/healthy/alerts_test.yml
    WARN NF204 examples/checkout/deploy/prometheusrule.yaml:15 [alert CheckoutHighErrorRate]
      the tests check a different version of this alert: examples/healthy/alerts.yml:10 has expr "job:checkout_requests_errors:ratio_rate5m > 0.05" for 10m, and this file has expr "job:checkout_requests_errors:ratio_rate5m > 0.02" for 10m
      fix: regenerate the tested rule file from this one, or point the tests at this file
    WARN NF203 examples/checkout/deploy/prometheusrule.yaml:25 [alert CartServiceDown]
      no test file loads this alert: examples/checkout/deploy/prometheusrule.yaml isn't in any test's rule_files, and no tested rule file has an alert named CartServiceDown
      fix: add the file to a test's rule_files and write a test that expects the alert to fire

    alert                  status             firing  quiet  rule
    CheckoutHighErrorRate  fires in a test         1      1  examples/healthy/alerts.yml:10
    CheckoutDown           fires in a test         1      1  examples/healthy/alerts.yml:18
    CheckoutHighErrorRate  fires in a test         1      1  examples/checkout/deploy/prometheusrule.yaml:15, tested as a different copy at examples/healthy/alerts.yml:10
    CheckoutDown           fires in a test         1      1  examples/checkout/deploy/prometheusrule.yaml:20, tested as examples/healthy/alerts.yml:18
    CartServiceDown        no test loads it        0      0  examples/checkout/deploy/prometheusrule.yaml:25

    neverfired: 1 test file, 2 rule files, 5 alerts (4 fires in a test, 1 no test loads it): 0 errors, 2 warnings, 0 notes

Add `--format json` for machine-readable output. `examples/README.md` says how the examples were made.

## Use it on your rules

Pass the same files you pass to `promtool test rules`. If `rule_files` point at generated files (a jsonnet mixin, a Helm chart), generate them first:

    promtool test rules tests/*.yml
    go run github.com/GillMacedo/neverfired/plugin/scripts@v0.1.0 tests/*.yml

To check the rules you deploy against the tested copies, add them with `--rules`, quoted so neverfired expands the glob. Plain rule files and PrometheusRule objects both work:

    go run github.com/GillMacedo/neverfired/plugin/scripts@v0.1.0 --rules 'deploy/rules/*.yaml' tests/*.yml

In CI, run it next to promtool, so a PR that renames a series, a label or an alert fails when it leaves a test checking nothing:

    - run: promtool test rules tests/*.yml
    - run: go run github.com/GillMacedo/neverfired/plugin/scripts@v0.1.0 tests/*.yml

The default `--fail-on error` fails on tests that can't fail. Use `--fail-on warn` to also fail on untested alerts.

## What it checks

| Code | Level | Meaning |
|---|---|---|
| NF101 | error | A `rule_files` entry matches no file. promtool prints a warning and reports SUCCESS |
| NF102 | error | A test names an alert that no loaded rule file defines. A "no alert" check for it always passes |
| NF103 | warn | A "no alert" check at an `eval_time` before the alert's `for` has elapsed, with no later check of that alert in the group. It tests only the `for` duration |
| NF104 | error | A "no alert" check where the expression can't return anything: series with the metric name exist but have other labels, or have no sample until after `eval_time` |
| NF105 | error | The same for a `promql_expr_test` case that expects no samples |
| NF106 | error | Expected labels or annotations the rule can't produce: a static label or annotation left out or with another value, or a label nothing in the test makes. promtool fails these, and a cut-off file looks the same |
| NF107 | warn | A "no alert" check where no input series has a metric the expression needs. Fine when the test is about that metric being absent |
| NF108 | warn | The same for a `promql_expr_test` case that expects no samples |
| NF201 | warn | An alert no test names |
| NF202 | warn | An alert that tests only expect to stay quiet. Nothing shows it ever fires |
| NF203 | warn | An alert in a `--rules` file that no test loads, by file or by name |
| NF204 | warn | An alert in a `--rules` file whose tested copy has a different expression or `for` |
| NF301 | note | An input series whose metric name no rule and no expression in its group reads, which is what a typo in the name looks like |

How "can't return anything" is decided:

- A selector gets data when an input series of the test group matches its name and all its label matchers and has a real sample (not `_` or `stale`) at or before `eval_time`. With `offset` or `@`, any real sample counts. A selector with a recording rule's name gets data when that rule's expression does, followed through chains of recording rules. `ALERTS` and `ALERTS_FOR_STATE` always count as having data.
- Aggregations and most functions return nothing when their series argument has no data. `absent`, `absent_over_time`, `vector`, `time`, `scalar` and `pi` always return something, and `info` follows its first argument.
- `and` and arithmetic or comparison operators return nothing when either side does; `or` and the `fill` modifiers only when both sides do; `unless` when its left side does.
- Anything else counts as returning something. Series that stop before `eval_time` and the 5 minute lookback are not used to claim emptiness, so a finding never depends on them.

## Exit codes

| Code | Meaning |
|---|---|
| 0 | Nothing at or above `--fail-on` (default `error`), or `--fail-on never` |
| 1 | At least one finding at or above `--fail-on` (`error`, `warn` or `note`) |
| 2 | The input can't be trusted, or the flags are wrong. Stdout is empty, whatever `--fail-on` says |

## When it refuses

neverfired exits 2 and prints only the reason on stderr when it can't trust what it read. For the healthy test file with its last 30 bytes cut off:

    neverfired: nothing was checked, because the input can't be trusted: test file alerts_test.yml doesn't end with a newline, so it may be cut off; if the file is complete, add a newline at the end

A test file, rule file or `--rules` file is refused when it is missing, a directory, empty, white space or comments only, binary, not valid UTF-8, not YAML, plain text, missing its final newline, the output of a failed `promtool`, `go`, `helm`, `kubectl`, `jsonnet` or `make` command, or more than one YAML document (except PrometheusRule objects in `--rules`). It is also refused when it has a field promtool doesn't know (unless you pass `--ignore-unknown-fields`, as with promtool), an expression or input series that doesn't parse, an unknown PromQL function, a duration or `values` string promtool would reject, a test file with no tests, a test group with no test cases, a test case or rule missing a required field, a rule file with no rules, or a PrometheusRule in `rule_files`, which promtool can't load. These checks run before anything is reported, under every `--fail-on` value and both formats.

## Limits

- **It doesn't evaluate PromQL.** Whether a test that expects an alert really gets it is promtool's job; run `promtool test rules` too. neverfired checks what a passing run means.
- **"Returns nothing" is the only claim about values.** A "no alert" check whose expression gets data but uses a threshold that could never be crossed isn't found.
- **A file cut at the end of a line** right after a complete test case or rule is a valid, shorter file. neverfired and promtool can't tell. In the healthy example, every such cut still produces findings.
- **NF107 can be deliberate.** A test that shows an alert stays quiet when a metric is missing looks the same as a test that forgot the metric. Read the test before changing it.
- **PromQL only.** Loki's LogQL rules aren't read; a LogQL expression is refused as unparsable. Thanos, Mimir and Cortex rule files use PromQL and work.
- **No type checking.** An expression with a type error, such as `rate(foo)`, is read as long as it parses. `promtool check rules` reports those.
- **Copies are matched by alert name.** With `--rules`, an alert in a file no test loads counts as tested when a tested file has an alert with the same name; NF204 says when their expression or `for` differ.

## FAQ

**How do I unit test Prometheus alerting rules?** Write a test file with `rule_files`, `input_series` and `alert_rule_test` cases, and run `promtool test rules` on it. The format is in the Prometheus docs under "Unit testing for rules". neverfired then checks that each alert has a test that makes it fire and that the "no alert" checks get data.

**Why does promtool test rules pass when my alert is broken?** A test that expects no alert passes whenever the alert doesn't fire, including when the alert name is wrong, the input series don't match the selectors, the rule file wasn't loaded, or `eval_time` is before `for`. neverfired reports each of these.

**How do I measure test coverage for Prometheus alert rules?** Run neverfired on your test files. The table lists every alert in the loaded rule files with the number of test cases that expect it to fire and to stay quiet. Add `--rules` with your deployed rule files to include alerts no test loads.

**Does it work with kube-prometheus, PrometheusRule objects and Helm charts?** promtool needs plain rule files in `rule_files`, so test against those. Pass the PrometheusRule manifests or the rendered chart with `--rules` to find alerts no test covers and copies that differ from the tested ones.

**Does it run promtool or connect to Prometheus?** No. It reads files only, needs no network, and runs no other program.

**Is it an MCP server?** No. It's a skill with one Go program, so `go run` or `go build` is all it needs.

## Tested on

- **The Prometheus query engine.** `tests/enginecheck` (run with `make engine-check`, in CI on every push) takes the 1,688 instant queries in Prometheus v0.316.0's engine tests that use one load interval. For each, it writes a promtool test file with the loaded series, and one test group without each metric name in turn, runs neverfired on it, and evaluates the same query with the Prometheus engine: 6,106 cases, of which neverfired called 1,472 empty, and the engine returned nothing for all 1,472. No query was refused.
- **The Prometheus parser.** During development, the parser was compared with Prometheus's parser on 830 distinct expressions from real rule files (kubernetes-mixin, Ceph, Thanos, Mimir, Loki's PromQL rules, kube-prometheus): the same selectors for all 830.
- **promtool.** promtool v0.316.0 (built from source) reports SUCCESS for both examples, for a misspelled alertname with no expected alerts, for a quiet check with no matching input, for a check before `for` elapses, and for a `rule_files` glob that matches nothing. It fails tests whose expected labels or annotations differ from the alert's, which is what NF106 relies on.
- **Real suites,** with Go 1.27.4 on linux/arm64:
  - kubernetes-monitoring/kubernetes-mixin at 2002557, rules generated with go-jsonnet: 10 test files, 65 alerts, 50 shown firing, 15 untested, no errors. Two NF107 findings in `tests/tests.yaml` (lines 402 and 418) are real: the tests meant to show that the warning-level `KubePersistentVolumeInodesFillingUp` ignores `ReadOnlyMany` volumes and volumes labeled `excluded_from_alerts` never feed `kubelet_volume_stats_inodes_used`, so the expression is empty whatever the access mode. Deleting either `unless` clause from that rule still gives SUCCESS in promtool; adding the missing series makes the deletion fail the test. Two more NF107 findings are deliberate tests of a missing `kube_node_info`.
  - ceph/ceph `monitoring/ceph-mixin` at f469bd8: 2 test files, 109 alerts, all shown firing, no findings. It stays quiet.
  - thanos-io/thanos `examples/alerts` at 9d58b7a: 48 alerts loaded, 2 shown firing, 46 untested (NF201), no errors.
- **Input it can't trust.** 32 bad test files with a real rule file, 29 bad rule files with a real test file, and 23 bad `--rules` inputs with both real: empty, white space, comments, binary, invalid UTF-8, plain text, other YAML, cut in half, cut inside a string, no final newline, output of failed promtool, go, jsonnet, helm, kubectl and make commands, a panic, misspelled fields, bad durations, values, series and expressions, missing files and directories. Each must exit 2 with nothing on stdout, under `--fail-on error` and `never` (and both formats for the first two). Every byte prefix of the healthy test file and rule file is also checked: cuts inside a line are refused, and no cut at a line end comes out clean.
- **Go versions.** The suite passes on Go 1.22.12 and 1.27.4, with `-race`.
- **The installed plugin.** A test copies `plugin/` out of the repository and builds it with the skill's own command.

Not tested: macOS and Windows; promtool versions other than v0.316.0; Loki, Mimir and Thanos rule files run through their own tools; a fresh Claude Code session using the skill. The `go run ...@v0.1.0` lines work only once the repository is published.

## Contributing

Contributions are welcome, from people and from people working with AI agents. See [CONTRIBUTING.md](CONTRIBUTING.md). Good first issues are [listed here](https://github.com/GillMacedo/neverfired/issues?q=is%3Aissue+is%3Aopen+label%3A%22good+first+issue%22).

### How pull requests work

1. **Open a PR.** Fork the repo, work on a branch, and keep it to one change. Write a title that says what changes for users, because it becomes the changelog line: `feat:` goes under Added and `fix:` under Fixed.
2. **Automatic check.** Code changes need tests, and `make test` must pass. Changes over 30 lines need a linked open issue (`Closes #<number>`), and a PR can have at most 800 changed lines. If something is missing, a comment tells you what to fix.
3. **Review.** Claude reviews each PR automatically. Code PRs also get a security review and are tested in a sandbox. The review then merges the PR or asks for changes, and a small problem may become a follow-up issue instead. A PR with no new commit 14 days after a change request is closed, and you can reopen it. PRs that change CI, agent settings, npm or Python dependencies, vendored code or build files can't be merged this way, and a comment says so. The same goes for anything the security review doesn't clear.
4. **Release.** A merge that changes what users install becomes a new version within about two hours, with a GitHub Release and, if the project has one, its npm package. Docs and CI changes wait for the next release.

## AI disclosure

This project was largely generated with Claude Code.

## About me

I'm Giulliano Macedo, and I build developer tools at GoTech Solutions. Follow along or get in touch:

- GitHub: [GillMacedo](https://github.com/GillMacedo)
- LinkedIn: [giulliano](https://www.linkedin.com/in/giulliano/)
- X: [@giumacedo](https://x.com/giumacedo)

If `neverfired` saved you some prompts, a star helps others find it.

---

Built by [Giulliano Macedo](https://github.com/GillMacedo), GoTech Solutions · [LinkedIn](https://www.linkedin.com/in/giulliano/) · [X](https://x.com/giumacedo)
