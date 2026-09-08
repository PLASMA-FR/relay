GO ?= go
VERSION ?= 0.2.0
LDFLAGS = -s -w -X github.com/PLASMA-FR/relay/internal/model.Version=$(VERSION)
.PHONY: build test race vet fmt check bench large-test install release clean
build:
	CGO_ENABLED=0 $(GO) build -trimpath -buildvcs=false -ldflags '$(LDFLAGS)' -o bin/relay ./cmd/relay
test:
	$(GO) test ./...
race:
	$(GO) test -race ./...
vet:
	$(GO) vet ./...
fmt:
	gofmt -w cmd internal
check: test vet
bench:
	$(GO) test ./internal/transfer ./internal/protocol -run '^$$' -bench . -benchmem
large-test:
	RELAY_LARGE_TEST=1 $(GO) test ./internal/transfer -run TestLarge -count=1 -timeout=30m -v
install:
	./install.sh --user
release:
	GO='$(GO)' ./scripts/release.sh '$(VERSION)'
clean:
	rm -rf bin dist
