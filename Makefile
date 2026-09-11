BINARY := weekly-insights
VERSION ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)
LDFLAGS := -s -w -X main.version=$(VERSION)

.PHONY: build test lint fmt install clean dist

build:
	go build -ldflags "$(LDFLAGS)" -o $(BINARY) ./cmd/weekly-insights

test:
	go test ./...

lint: fmt
	go vet ./...

fmt:
	gofmt -l -w .

install:
	go install -ldflags "$(LDFLAGS)" ./cmd/weekly-insights

clean:
	rm -rf $(BINARY) dist/

# Cross-compiled release artifacts. Pure stdlib and no cgo, so every target
# builds from any host without a toolchain per platform.
dist: clean
	@mkdir -p dist
	@for t in darwin/arm64 darwin/amd64 linux/amd64 linux/arm64; do \
		os=$${t%/*}; arch=$${t#*/}; \
		CGO_ENABLED=0 GOOS=$$os GOARCH=$$arch \
			go build -ldflags "$(LDFLAGS)" -o dist/$(BINARY)-$$os-$$arch ./cmd/weekly-insights || exit 1; \
	done
	@ls -1 dist/
