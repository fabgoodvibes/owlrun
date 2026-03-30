# Contributing to Owlrun

Thanks for your interest in contributing to Owlrun!

## Building

Requires Go 1.25+.

```bash
# Native build
make build

# All platforms (Windows, Linux amd64/arm64, macOS amd64/arm64)
make build-all

# Cross-compile for Windows from Linux/macOS
GOOS=windows GOARCH=amd64 CGO_ENABLED=0 go build -o dist/owlrun.exe ./cmd/owlrun
```

## Running tests

```bash
make test
# or
go test ./...
```

All tests must pass before submitting a PR.

## Code style

- Standard Go: `gofmt` formatting, `go vet` clean
- No CGO required for Windows or macOS builds
- Platform-specific code uses build tags: `_windows.go` + `_other.go` (or `_linux.go`, `_darwin.go`)

## Pull requests

1. Fork the repo and create a feature branch from `dev`
2. Make your changes
3. Run `go vet ./...` and `go test ./...`
4. Submit a PR against `dev` with a clear description of the change

## Reporting issues

Open an issue on GitHub with:
- Your OS and GPU (vendor, model, VRAM)
- Owlrun version (`owlrun --version`)
- Steps to reproduce
- Relevant logs from `~/.owlrun/owlrun.log`

## License

By contributing, you agree that your contributions will be licensed under the [MIT License](LICENSE).
