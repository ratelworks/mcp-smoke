BIN_NAME := mcp-smoke
GO := go

.PHONY: build test lint clean

build:
	$(GO) build -o bin/$(BIN_NAME) .

test:
	$(GO) test -race ./...

lint:
	golangci-lint run ./...

clean:
	rm -rf bin
