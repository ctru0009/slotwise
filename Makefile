.PHONY: generate fmt lint test test-int vuln check check-all
generate:
	@echo "nothing to generate yet"
fmt:
	go tool gofumpt -w .
lint:
	go tool golangci-lint run ./...
test:
	go test -race -shuffle=on -count=1 ./...
test-int:
	go test -race -shuffle=on -count=1 -tags=integration ./...
vuln:
	go tool govulncheck ./...
check: generate fmt lint test test-int vuln
	go mod tidy -diff
# Reports every failing stage in one pass; check stops at the first failure.
check-all:
	$(MAKE) -k check
