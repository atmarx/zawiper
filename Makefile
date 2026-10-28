VERSION := 0.2.0
COMMIT  := $(shell git describe --always --dirty 2>/dev/null || echo unknown)
LDFLAGS := -s -w -X main.version=$(VERSION) -X main.commit=$(COMMIT)
GOBUILD := CGO_ENABLED=0 go build -trimpath -ldflags "$(LDFLAGS)"
# The bundle lands in bin/, which git ignores.
BUNDLE  := bin/zawiper-bay-$(VERSION)-$(COMMIT).tar.gz

.PHONY: build test bundle bay image

build:
	$(GOBUILD) -o bin/bayd ./cmd/bayd
	$(GOBUILD) -o bin/bayd-link ./cmd/bayd-link
	$(GOBUILD) -o bin/zaweb ./cmd/zaweb
	$(GOBUILD) -o bin/bayrules ./deploy/bayrules

test:
	go vet ./...
	go test ./...

# What a dock host gets: bayd, bayd-link, the rule generator, the units and
# install-bay.sh.  Never zaweb, and never through CI (spec §8): a bundle
# goes out by fleet play or by hand.
bundle: test build
	tar -czf $(BUNDLE) bin/bayd bin/bayd-link bin/bayrules \
		deploy/install-bay.sh deploy/systemd deploy/sysusers.d
	sha256sum $(BUNDLE)

# By hand: copy the bundle to a dock host and install it with your own sudo.
bay: bundle
	@test -n "$(HOST)" || { echo "make bay HOST=<dock host>"; exit 2; }
	ssh $(HOST) 'rm -rf zawiper-bay && mkdir zawiper-bay'
	ssh $(HOST) 'tar -xzf - -C zawiper-bay' <$(BUNDLE)
	ssh -t $(HOST) 'sudo zawiper-bay/deploy/install-bay.sh $(if $(HUB),--hub $(HUB))'

# The hub's image, as CI builds it on the hub host.
image:
	docker build -f deploy/Dockerfile --build-arg VERSION=$(VERSION) \
		--build-arg COMMIT=$(COMMIT) -t zawiper/zaweb:latest .
