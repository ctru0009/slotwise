.PHONY: generate fmt lint test test-int vuln check check-all
generate:
	go tool sqlc generate
fmt:
	go tool gofumpt -w .
lint:
	go tool golangci-lint run ./...
test:
	go test -race -shuffle=on -count=1 ./...
# Every integration test starts its own Postgres. Ten of them at once on a
# laptop's Docker starved container startup and failed the suite on
# infrastructure, not assertions; four keeps peak memory sane and matches the
# four cores a GitHub runner has, so the gate behaves the same in both places.
test-int:
	go test -race -shuffle=on -count=1 -parallel=4 -tags=integration ./...
vuln:
	go tool govulncheck ./...
check: generate fmt lint test test-int vuln
	go mod tidy -diff
# Reports every failing stage in one pass; check stops at the first failure.
check-all:
	$(MAKE) -k check
