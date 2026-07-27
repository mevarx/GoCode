.PHONY: build test lint install clean

BINARY=gocode
PKG=./cmd/gocode/

build:
	go build -o bin/$(BINARY) $(PKG)

test:
	go test ./... -race -coverprofile=coverage.out

lint:
	go vet ./...
	gofmt -l .

install:
	go install $(PKG)

clean:
	rm -rf bin/ dist/ coverage.out
