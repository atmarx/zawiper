# Za Wiper — Project Guide

## What Is This

Drive bays that wipe, prove it, and remember.  Bays on any number of dock hosts dial out to one hub, which keeps the wall, the records and the signing key.  The record is the product: it names the sanitization level, technique and standard clause, and it has to hold up for a stranger who doesn't trust us.  Spun out of [The Ark](https://github.com/atmarx/the-ark) on 2026-10-27, history intact; Wipe + Load (an Ark loadout on a wiped drive) is Phase 3.

- `docs/spec.md` is the design and the decisions, with who made each call and when.  Keep it current with the code.
- `docs/bringup.md` takes a newcomer from nothing to a verified record.  `docs/operating.md` is day to day.

## Layout

```
cmd/bayd         disk side, one per bay, root, DevicePolicy=closed, no network
cmd/bayd-link    network side, one per bay, mTLS to the hub, no devices
cmd/zaweb        the hub: wall, records, signing (a container)
cmd/zverify      a buyer's check of a drive against its record
cmd/zawipe       the hub in plain text: the wall as DIR, a bay scrolling like CHKDSK
deploy/          bayrules (udev + drop-ins), install-bay.sh, ca.sh, units, hub compose
site.example/    a deployment's overlay, copied to site/ (gitignored): hub .env, override, bay configs
internal/        bay, link, hub, web, wipe, smart, grade, records, sign, proto, config, model
```

## Invariants

- **Can't be pointed at the wrong disk.**  This outranks everything.  A bay's scope is held by the kernel (`DevicePolicy=closed`, `/dev/zawiper/<bay>` only), never by bayd's good intentions.
- **No abort on the page.**  Stopping a run takes root on the dock host (`systemctl stop bayd@<bay>`).
- **Bays never ship through CI.**  `make bay HOST=` or the installer, under a person's own sudo.  CI deploys the hub only.  The installer never restarts a running `bayd@`.
- **Never claim more than happened.**  Flash gets no level from an overwrite.  No Purge unless the drive's sanitize command ran and got through the bridge.
- **Keys stay out of git.**  `deploy/ca.sh` refuses to write inside a work tree.  The CA root password is typed by a person in a real terminal, never through an agent's transcript.
- **Deployment-agnostic.**  No hostnames, addresses or org names in code, defaults or docs.  A deployment's specifics live in its `site/` (from `site.example/`, never committed here) and get pushed to its hosts: `/opt/zawiper/.env` and `compose.override.yml` on the hub, `/etc/zawiper/bays/` on dock hosts.  No keys in `site/`.  Examples use `example.org` and `192.0.2.0/24`.

## Working on it

```
make test                                   # go vet + go test
make build                                  # bin/bayd, bin/bayd-link, bin/zaweb, bin/zawipe, bin/bayrules
bin/zaweb -demo -hosts '' -records /tmp/zw  # the wall with a pretend fleet
```

- Go 1.24, `CGO_ENABLED=0`.  gofmt and vet clean before a commit.
- `.woodpecker/hub.yml`: a push to master that touches code tests, builds and deploys the hub.  Use `[skip ci]` when the hub doesn't need it.
- Prose in docs and on the page: plain words, double spaces after periods.
