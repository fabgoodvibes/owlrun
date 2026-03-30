VERSION ?= dev

.PHONY: build build-all test vet clean

build:
	CGO_ENABLED=0 go build -ldflags="-s -w -X main.version=$(VERSION)" -o dist/owlrun ./cmd/owlrun

build-all: build-windows build-linux build-linux-arm64 build-darwin build-darwin-arm64

build-windows:
	GOOS=windows GOARCH=amd64 CGO_ENABLED=0 go build -ldflags="-s -w -X main.version=$(VERSION)" -o dist/owlrun.exe ./cmd/owlrun

build-linux:
	GOOS=linux GOARCH=amd64 CGO_ENABLED=0 go build -ldflags="-s -w -X main.version=$(VERSION)" -o dist/owlrun-linux-amd64 ./cmd/owlrun

build-linux-arm64:
	GOOS=linux GOARCH=arm64 CGO_ENABLED=0 go build -ldflags="-s -w -X main.version=$(VERSION)" -o dist/owlrun-linux-arm64 ./cmd/owlrun

build-darwin:
	GOOS=darwin GOARCH=amd64 CGO_ENABLED=0 go build -ldflags="-s -w -X main.version=$(VERSION)" -o dist/owlrun-darwin-amd64 ./cmd/owlrun

build-darwin-arm64:
	GOOS=darwin GOARCH=arm64 CGO_ENABLED=0 go build -ldflags="-s -w -X main.version=$(VERSION)" -o dist/owlrun-darwin-arm64 ./cmd/owlrun

test:
	go test ./...

vet:
	go vet ./...

clean:
	rm -rf dist/
