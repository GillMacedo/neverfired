# 0002: Its own PromQL parser, checked against Prometheus

**Decision:** neverfired parses PromQL with a small parser in `promql.go` instead of importing `github.com/prometheus/prometheus/promql/parser`.

**Why:** the Prometheus module pulls in a large dependency tree and needs a recent Go, which would break the plugin's offline `go build` from its own directory and the Go 1.22 floor. neverfired needs selectors and the expression's shape, not types or evaluation.

**How it stays right:** `tests/enginecheck` is a separate module that does import Prometheus. It runs neverfired on promtool files built from every instant query in Prometheus's engine tests and requires the engine to agree with every "returns nothing" claim. During development the parser was also compared with Prometheus's parser on 830 real rule expressions (kubernetes-mixin, Ceph, Thanos, Mimir, Loki, kube-prometheus) with the same selectors for all of them.

**Cost:** new PromQL syntax has to be added by hand. Until it is, a rule that uses it is refused with exit 2, never misread.
