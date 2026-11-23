# Roadmap

## 0.1.0 (this release)
- Read promtool test files and the rule files they load, and list each alert as shown firing, tested only as quiet, untested, or not loaded by any test.
- Report tests that can't fail: alertnames no rule defines, `rule_files` globs that match nothing, quiet checks whose expression gets no data, quiet checks before `for` elapses, and expected labels or annotations promtool can't match.
- Compare deployed rules (`--rules`, plain rule files or PrometheusRule objects) with the tested copies.

## Next
- Inline ignores for findings that are deliberate, such as a test about a missing metric (NF107), with a comment on the test case.
- A directory argument that finds test files and rule files by their shape.
- Recording rule coverage: recording rules no `promql_expr_test` and no tested alert reads.
- Quiet checks whose series stop more than the 5 minute lookback before `eval_time`.
- SARIF output for GitHub code scanning.

## Not planned
- Evaluating PromQL or running promtool. promtool already checks that tests pass; neverfired checks what a pass means.
- Changing test or rule files.
