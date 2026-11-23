# neverfired

Claude Code skill and Go CLI that reads promtool rule unit tests and the rule files they load, and reports alerts no test shows firing and tests that can't fail.

## Architecture
`plugin/scripts` is one `package main` with no dependencies outside the standard library, so the installed plugin builds with `go build` alone. `promql.go` is a small PromQL parser that keeps selectors and expression shape; `load.go` reads test files, rule files and PrometheusRule objects and refuses input it can't trust; `check.go` decides which tests can't fail and builds the coverage table; `neverfired.go` is the CLI. `yaml_*.go` is go.yaml.in/yaml/v3 v3.0.4 copied with the package renamed. `plugin/skills/checking-promtool-alert-tests/SKILL.md` tells Claude how to use it. `tests/enginecheck` is a separate module that checks the tool against the Prometheus query engine. See `docs/architecture.md`.

## Commands
- `make test`: gofmt, vet and tests with `-race`
- `make engine-check`: compare neverfired with the Prometheus engine (downloads the Prometheus module the first time; needs Go 1.26.7 or newer)
- `make lint`: plugin and marketplace manifest validation (needs Claude Code)
- `make demo`: run the README examples

## Conventions
- Standard library only in `plugin/scripts`. Never run promtool or Prometheus from the tool.
- Each check has a stable `NFnnn` code: NF1xx tests that can't fail or are weak, NF2xx coverage, NF3xx notes. Don't reuse codes. Every code is in the README table and the skill.
- Only claim an expression returns nothing when that is certain; answer "not sure" otherwise. `tests/enginecheck` must keep passing.
- Input it can't trust exits 2 with nothing on stdout, under every `--fail-on` value. Each new kind of input needs a test for empty, garbage, cut off and failed-command output.

## Do and don't
- Do add a case to `examples/` or `tests/` for each new check, with a healthy case that stays quiet.
- Do run a new check on a real suite (kubernetes-mixin, Ceph, Thanos) and read what it flags before adding it.
- Don't say alerts are well tested. Say what was checked and what wasn't.
- Don't edit `yaml_*.go` except to update the copied version.

## Definition of done
`make test` and `make lint` pass, README examples match real output (`TestReadme`), and `ROADMAP.md`, `CHANGELOG.md` and docs are updated when relevant.
