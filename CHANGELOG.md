# Changelog

All notable changes are documented here, in [Keep a Changelog](https://keepachangelog.com/en/1.1.0/) format. This project uses [Semantic Versioning](https://semver.org/).

## [Unreleased]

## [0.1.0] - 2026-11-23

### Added
- `neverfired` CLI and the `checking-promtool-alert-tests` skill: read promtool rule unit test files and the rule files they load, and report alerts no test shows firing and tests that can't fail.
- Tests that can't fail: an alertname no loaded rule defines (NF102), a `rule_files` entry that matches no file (NF101), a quiet check whose expression gets no data because of wrong labels or late samples (NF104, NF105), and expected labels or annotations the rule can't produce (NF106).
- Weak tests: a quiet check before the alert's `for` elapses (NF103), and quiet checks where a metric the expression needs is missing from the input (NF107, NF108).
- Coverage: alerts no test names (NF201), alerts tested only as quiet (NF202), and with `--rules`, alerts no test loads (NF203) and deployed alerts that differ from the tested copy (NF204). Input series no rule reads (NF301).
- A PromQL parser for Prometheus 3 syntax, checked against the Prometheus parser and query engine.
- `--rules`, `--format text|json`, `--fail-on error|warn|note|never` and `--ignore-unknown-fields`.
- Refuses with exit 2 when a file is missing, empty, binary, not YAML, cut off, a command's error output, has a misspelled field or an expression that doesn't parse, or tests nothing.
