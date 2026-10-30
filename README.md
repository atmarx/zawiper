# Za Wiper

**Drive bays that wipe, prove it, and remember.**

A cart of retired drives, a USB dock in a lab down the hall, and an auditor who wants to know, two years from now, what happened to serial `Z1N4BVB3`.  Za Wiper is for that whole chain: the bay that wipes the drive, the record that says exactly how, and the one place every record comes back to.

Put a drive in a bay.  It reads the drive's identity, SMART health and whatever is on it, and touches nothing.  Confirm, and it overwrites every sector, reads every sector back, compares SMART before and after, and files a signed record under the drive's own serial number.  The record prints as a certificate, and anyone can check it without trusting us.

## What a record proves

- **The level, the technique and the clause.**  Za Wiper targets NIST SP 800-88 Rev. 2, which defers to IEEE 2883-2022.  A hard drive overwritten with full read-back is **Clear** (IEEE 2883 §8.4.2.2).  The record says so, and names anything that stopped it from qualifying.
- **Nothing was hidden.**  Before an ATA drive is overwritten, `hdparm` asks it about a host protected area or a device configuration overlay.  A drive keeping sectors back is refused until they're restored, because a wipe that can't reach them isn't Clear.
- **Every byte went back out.**  The wipe writes a keystream, not zeros, and reads the whole drive back against it.  The key is published in the record once the wipe is done, so `zverify` can regenerate the stream and hold the drive up against it later: sampled the way IEEE 2883 §7.3 suggests, or every byte.
- **The drive's own health,** before and after, graded PASS, PASS WITH NOTES or FAIL, with the evidence.
- **That we wrote it.**  The hub signs each record as it arrives (SSH signature, Ed25519).  `ssh-keygen -Y verify` checks it against the hub's published key, with none of our code in the loop.  A record's `proof.zip` carries everything a buyer or an auditor needs to check it offline.

What it won't claim: anything for flash.  An overwrite proves nothing on an SSD, so solid-state drives get no level from it.  Purge through the drive's own sanitize command is where that goes next.  Za Wiper never claims Purge when the command wasn't available, or didn't make it through the bridge.

## How it scales

It runs fine as one PC with one USB dock.  The point is when it isn't: a bay in every lab that retires drives, and every record coming home to one hub, so there's one place to ask what happened to a serial, which machine wiped it, how, and when.

```
  lab A: a two-slot USB dock          lab B: a SAS HBA port
 ┌───────────────────────────┐       ┌───────────────────────────┐
 │ bayd@slot1   bayd@slot2   │       │ bayd@hba0                 │
 │ bayd-link@   bayd-link@   │       │ bayd-link@hba0            │
 └─────────────┬─────────────┘       └─────────────┬─────────────┘
               │      mTLS, dialing out only       │
               └─────────────────┬─────────────────┘
                                 ▼
                    ┌─────────────────────────┐
                    │ the hub: zaweb          │
                    │ the wall, every record, │
                    │ the signing key         │
                    └─────────────────────────┘
```

- **A bay is a slot**: one slot of a USB dock, a USB-SAS adapter, or a port on a SAS card in IT mode.  It takes a config file, a client certificate, and a run of the installer.  Any lab with a spare machine and a dock can have one.
- **Bays dial out, and listen on nothing.**  Each bay's link holds a mutual-TLS connection to the hub with that bay's own certificate.  A dock host needs no inbound ports.
- **The kernel holds each bay to its drive.**  The disk side, `bayd@<bay>`, runs with systemd's `DevicePolicy=closed`.  Of every disk on the host it can open only the one in its own slot, even as root, and it has no network.  The link has the network and no devices.
- **History follows the drive, not the dock.**  Records are filed by serial number on the hub.  A drive read in one lab and wiped in another has one history, searchable from one page, with the host and bay that ran each job.
- **Everyone sees the same wall,** a page with every bay on it and the live surface map of each wipe in progress, drawn like MS-DOS DEFRAG.  There's no abort button, on purpose.  A run that anyone watching could stop is a run nobody can vouch for.

## Hard to point at the wrong disk

That's the first goal, and it outranks everything else.  A bay refuses a drive the host is using, two drives on one port, a drive that won't say who it is, a drive whose size the bridge and the drive disagree on, and a drive with hidden sectors.  A wipe has to name the serial and the insertion its confirmation was drawn from, so a stale page or a swapped drive fails closed.  Backup media (a `backup-` label, a restic repository, a PBS datastore, an md, LVM or ZFS member) turns the card amber, and the button reads **Erase this backup drive**.

## And The Ark

A wiped drive doesn't have to leave empty.  [The Ark](https://git.example.org/atmarx/the-ark) builds offline libraries onto ordinary drives: Wikipedia, Project Gutenberg, medical references, repair guides, with the Kiwix readers on board.  Za Wiper started there as the bay that prepares a drive for an Ark, and **Wipe + Load** is the job that joins them: wipe, prove it, then lay down a loadout and fill the drive to the last byte.  A retired drive leaves as a library, with a certificate that says what was on it before was erased.

Today the two meet by hand (`ark stamp` at a shell).  Wipe + Load in the bay is Phase 3.

## Where it stands

| | |
|---|---|
| **Built** | The read-only bay, the networked fleet with kernel-held scope, the wipe with full read-back, the HPA and DCO check, the SMART diff and grade, signed records, `proof.zip`, `zverify`, the wall.  First hardware wipe 2026-10-26. |
| **Next** | Purge through firmware sanitize, where the drive and bridge allow it.  Wipe + Load.  Certificate revocation per bay. |
| **For institutions** | Sign-in through your identity provider, with control set per host and visibility org-wide: each dock host belongs to the group allowed to run its bays (a Keycloak group, a Globus group, whatever your IdP carries), while anyone signed in can watch the whole wall and look up any serial's history.  Custody fields (asset tag, ticket, owner, disposition), hash-chained records, and destruction records for drives that can't be wiped.  Designed in the spec, not built yet: today, anyone who can reach the page can start a wipe, so keep it on a network where that's fine. |

## Try it without hardware

```
make test
make build
bin/zaweb -demo -hosts '' -records /tmp/zw    # a pretend fleet of four bays, on :8460
```

A "drive" can be a file: `truncate -s 1000204886016 /tmp/bay.img`, then run `bin/bayd -dev-bay /tmp/bay.img -dev-smart internal/smart/testdata/st31000528as.json`.  Delete the file to pull the drive.

## Docs

- [docs/bringup.md](docs/bringup.md): from nothing to a signed record, for anyone rolling it out.
- [site.example/](site.example/README.md): your deployment's settings, in one folder you copy and fill in.
- [docs/operating.md](docs/operating.md): day to day.  Adding and retiring bays, checking scope, holding a bay.
- [docs/spec.md](docs/spec.md): what it is, the decisions and why, the hardware lessons, the record format, the phases.

Go, no cgo.  A dock host needs Linux with systemd and udev, `smartmontools`, `hdparm` and `util-linux`.  The hub runs as a container.

## License

Apache 2.0.  See [LICENSE](LICENSE) and [NOTICE](NOTICE).  The surface map's font, IBM VGA 8×16 from VileR's Ultimate Oldschool PC Font Pack, keeps its own license, CC BY-SA 4.0.
