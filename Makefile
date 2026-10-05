.PHONY: generate fmt lint test test-int vuln check
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
