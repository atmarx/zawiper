VERSION := 0.1.0
COMMIT  := $(shell git describe --always --dirty 2>/dev/null || echo unknown)
LDFLAGS := -s -w -X main.version=$(VERSION) -X main.commit=$(COMMIT)
HOST    ?= 192.0.2.18

.PHONY: build test deploy

build:
	CGO_ENABLED=0 go build -trimpath -ldflags "$(LDFLAGS)" -o bin/bayd ./cmd/bayd
	CGO_ENABLED=0 go build -trimpath -ldflags "$(LDFLAGS)" -o bin/zaweb ./cmd/zaweb
	CGO_ENABLED=0 go build -trimpath -ldflags "$(LDFLAGS)" -o bin/zwipe ./cmd/zwipe

test:
	go vet ./...
	go test ./...

# Until Woodpecker has a lane: copy to dock-host and run deploy/install.sh.
deploy: test build
	tar -czf - bin deploy | ssh $(HOST) 'rm -rf zawiper && mkdir zawiper && tar -xzf - -C zawiper'
	ssh $(HOST) 'cd zawiper && sudo deploy/install.sh'
