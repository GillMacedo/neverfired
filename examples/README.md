# Examples

Both examples are made-up alert rules for an online shop's checkout service, written for this repository. Their promtool tests pass with promtool from Prometheus v0.316.0 (`promtool test rules examples/checkout/alerts_test.yml examples/healthy/alerts_test.yml`), which prints SUCCESS for both, with a warning about `shared/*.yml` for the first.

- `checkout/alerts.yml` and `checkout/alerts_test.yml`: a suite with problems on purpose. A quiet check feeds `status` labels to a rule that reads `code`, a quiet check runs before `for: 5m` elapses, one alertname is misspelled, one `rule_files` glob matches nothing, and two alerts have no test.
- `checkout/deploy/prometheusrule.yaml`: the same rules as deployed, with a lower error threshold and an extra alert no test loads, for `--rules`.
- `healthy/`: the same service with a complete suite. neverfired reports nothing for it under every `--fail-on` value.
