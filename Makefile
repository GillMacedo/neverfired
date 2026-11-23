.PHONY: test lint demo engine-check

test:
	test -z "$$(gofmt -l plugin tests)"
	go vet ./...
	go test -race -count=1 ./...

# Compares neverfired with the Prometheus query engine. Needs network the
# first time, to download the Prometheus module, and Go 1.26.7 or newer.
engine-check:
	cd tests/enginecheck && go test -count=1 ./...

lint:
	claude plugin validate ./plugin --strict
	claude plugin validate . --strict

# The checkout example has problems on purpose, so it runs with --fail-on never.
demo:
	go run ./plugin/scripts --fail-on never examples/checkout/alerts_test.yml
	go run ./plugin/scripts examples/healthy/alerts_test.yml
	go run ./plugin/scripts --fail-on never --rules 'examples/checkout/deploy/*.yaml' examples/checkout/alerts_test.yml
