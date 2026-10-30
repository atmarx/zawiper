# Contributing

Za Wiper erases disks, and its records have to hold up for a stranger who doesn't trust us.  So the bar here is a little odd: a feature that makes a wipe easier to aim wrong, or a record claim more than happened, doesn't get in however nice it is.

## The most useful thing you can send

A hardware report.  USB-SATA bridges decide what a bay can do, and no spec sheet tells you.  Run these on a drive you can read (they change nothing):

```
sudo smartctl -d sat -i /dev/sdX
sudo hdparm -N /dev/sdX
sudo hdparm --sanitize-status /dev/sdX
```

Then open a [hardware report](https://github.com/atmarx/zawiper/issues/new?template=hardware_report.yml) with the bridge's USB ID (`lsusb`), the drive's model, and what came back.  A bridge that fails is as useful as one that works.  Leave the serial out if you'd rather.

## Code

```
make test    # go vet + go test
make build
bin/zaweb -demo -hosts '' -records /tmp/zw    # the wall, with a pretend fleet
```

Go 1.24, no cgo, gofmt and vet clean.  You don't need a dock: a file can stand in for a drive (see the README).

Before you start on something big, open an issue.  [docs/spec.md](docs/spec.md) records what's been decided and why, and some things that look missing are missing on purpose.

## What won't change

- **Can't be pointed at the wrong disk.**  This outranks everything.  A bay's scope is held by the kernel, never by bayd's good intentions.
- **No abort on the page.**  A run that anyone watching could stop is a run nobody can vouch for.
- **Bays never ship through CI.**  A key that can install bayd can wipe whatever is in a bay.
- **Never claim more than happened.**  Flash gets no level from an overwrite.  No Purge unless the drive's sanitize command ran and got through the bridge.
- **Keys stay out of git.**
- **Deployment-agnostic.**  No hostnames, addresses or org names in code, defaults or docs.  Examples use `example.org` and `192.0.2.0/24`.

## Tests touching real disks

Never.  Tests use files and fixtures, and they have to pass on a machine whose disks you care about.  If you're testing a change on real hardware, use a drive you've already written off, in a bay of its own.

## Writing

Plain words in docs and on the page.  Double spaces after periods.

## AI

This project is written with an AI, and says so.  Use one if you like, and say so in the commit.  You still answer for what it wrote.

By contributing, you agree your work is licensed under the [Apache License 2.0](LICENSE).
