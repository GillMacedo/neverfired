# Security

Report vulnerabilities through [GitHub's private vulnerability reporting](https://github.com/GillMacedo/neverfired/security/advisories/new), not in a public issue.

`neverfired` reads the promtool test files you name, the rule files their `rule_files` entries match, and the files you pass with `--rules`. It doesn't run promtool, Prometheus or any other program, doesn't connect to the network, and doesn't write files. Alert names, label values, series and file paths are printed in findings, so don't share its output if those hold anything private.

A missed or wrong finding is a normal bug. If `neverfired` writes or changes a file, or reports a clean result for input it couldn't read, report it privately. `neverfired` is a diagnostic aid and doesn't enforce anything.
