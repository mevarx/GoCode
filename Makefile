.PHONY: build test lint install clean

BINARY=gocode
PKG=./cmd/gocode/

build:
	go build -o bin/$(BINARY) $(PKG)

test:
	@if command -v gcc >/dev/null 2>&1 && [ "$$(go env CGO_ENABLED)" = "1" ]; then \
		go test ./... -race -coverprofile=coverage.out; \
	else \
		echo "note: -race is unavailable (requires cgo and gcc); running tests without the race detector"; \
		go test ./... -coverprofile=coverage.out; \
	fi

lint:
	go vet ./...
	gofmt -l .

install:
	go install $(PKG)

clean:
	rm -rf bin/ dist/ coverage.out
