# Security

## Reporting

Please don't open a public issue.  Use [GitHub's private reporting](https://github.com/atmarx/zawiper/security/advisories/new).

This is a small project with one maintainer.  There's no bounty and no promised response time, but a report that can destroy someone's data goes to the top of the pile.

## What counts

Most urgent first:

1. **A bay wipes a drive it shouldn't.**  The wrong disk, one in use, a swapped drive, a stale confirmation, or anything that gets `bayd@<bay>` past its `DevicePolicy=closed` scope.
2. **A record lies.**  A forged or altered record that verifies, a level claimed that the wipe didn't earn, or data left on a drive that `zverify` passes.
3. **A bay reachable from where it shouldn't be.**  Past mTLS, one bay's certificate acting as another, or a way from the hub to a device.
4. **Key material leaking,** from the signing key to a client certificate.

## Known, and not a vulnerability

- **The page has no login yet.**  Anyone who can reach it can start a wipe.  The README says so, and sign-in is designed but not built.  Keep it on a network where that's fine, and don't put it on the internet.
- **An overwrite proves nothing on flash,** so solid-state drives get no level from one.  That's the record being honest.
- **Certificates can't be revoked per bay yet.**  It's on the list.

## Versions

Only the latest commit on `master` gets fixes.  There are no release branches yet.
