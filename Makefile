.PHONY: test test-unit test-integration tidy

test: test-unit test-integration

test-unit:
	go test -race -short ./internal/...

test-integration:
	go test -race -run Integration ./internal/...

tidy:
	go mod tidy
