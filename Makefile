VERSION := 0.2.0
COMMIT  := $(shell git describe --always --dirty 2>/dev/null || echo unknown)
LDFLAGS := -s -w -X main.version=$(VERSION) -X main.commit=$(COMMIT)
GOBUILD := CGO_ENABLED=0 go build -trimpath -ldflags "$(LDFLAGS)"
# The bundle lands in bin/, which git ignores.
BUNDLE  := bin/zawiper-bay-$(VERSION)-$(COMMIT).tar.gz

# Your deployment: HUB and HUB_HOST (site.example/README.md).  Anything set
# on the command line wins.
-include site/site.env

.PHONY: build test bundle bay image site hub-site bay-config need-host

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
bay: need-host bundle
	ssh $(HOST) 'rm -rf zawiper-bay && mkdir zawiper-bay'
	ssh $(HOST) 'tar -xzf - -C zawiper-bay' <$(BUNDLE)
	ssh -t $(HOST) 'sudo zawiper-bay/deploy/install-bay.sh $(if $(HUB),--hub $(HUB))'

# The hub's image, as CI builds it on the hub host.
image:
	docker build -f deploy/Dockerfile --build-arg VERSION=$(VERSION) \
		--build-arg COMMIT=$(COMMIT) -t zawiper/zaweb:latest .

need-host:
	@test -n "$(HOST)" || { echo "say which dock host: HOST=<host>"; exit 2; }

# Your site folder, from the template, once.
site:
	@test ! -e site || { echo "site/ is already there; edit it"; exit 1; }
	cp -r site.example site
	@echo "Fill in site/site.env, site/hub/ and site/bays/.  Git ignores site/."

# site/hub/ to /opt/zawiper on the hub host, under your own sudo.
hub-site:
	@test -n "$(HUB_HOST)" || { echo "set HUB_HOST in site/site.env"; exit 2; }
	@test -f site/hub/.env || { echo "no site/hub/.env (make site)"; exit 2; }
	ssh $(HUB_HOST) 'rm -rf zawiper-site && mkdir zawiper-site'
	tar -C site/hub -cf - . | ssh $(HUB_HOST) 'tar -xf - -C zawiper-site'
	ssh -t $(HUB_HOST) 'sudo install -d /opt/zawiper && sudo install -m 0644 zawiper-site/.env /opt/zawiper/.env && \
		{ test ! -f zawiper-site/compose.override.yml || sudo install -m 0644 zawiper-site/compose.override.yml /opt/zawiper/; } && \
		rm -rf zawiper-site'
	@echo "Restart the hub to pick it up: cd /opt/zawiper && sudo docker compose up -d"

# site/bays/<host>/*.toml to /etc/zawiper/bays on that host, checked first
# by bayd's own parser.  It adds and updates; it never deletes a config.
bay-config: need-host
	@test -d site/bays/$(HOST) || { echo "no site/bays/$(HOST)/"; exit 2; }
	$(GOBUILD) -o bin/bayrules ./deploy/bayrules
	bin/bayrules -bays site/bays/$(HOST) -check
	ssh $(HOST) 'rm -rf zawiper-bays && mkdir zawiper-bays'
	tar -C site/bays/$(HOST) -cf - . | ssh $(HOST) 'tar -xf - -C zawiper-bays'
	ssh -t $(HOST) 'sudo install -d -m 0755 /etc/zawiper/bays && sudo install -m 0644 zawiper-bays/*.toml /etc/zawiper/bays/ && rm -rf zawiper-bays'
