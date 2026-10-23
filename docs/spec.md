# Za Wiper — a drive bay that reads, wipes, proves, and fills with an Ark

Spec v0.2, 2026-10-23.  Zim, for The Ark.  Idea and name: xram.

v0.1 was written 2026-10-09 in the homelab repo (`docs/specs/za-wiper.md` at `d73b3bc`).  This version moves it home and folds in xram's calls of 2026-10-23 and Val's Phase 0 hardware facts.  Where v0.1 had more detail than this, and that detail still holds, this doc says so by section number, e.g. *v0.1 §8.3*.

## 1. What it is

One USB drive caddy on dev-host, passed through by port to `dock-host` (192.0.2.18).  A local-only web page at **`zawiper.example.org`** watches it.

1. **Put a drive in.**  Its read-only facts appear on their own: identity, SMART, the filesystems on it, and how much file data they hold.  The button on the page is **Re-read**.
2. **Look, then decide.**  If it's okay to wipe, press **Wipe** or **Wipe + Load**.  The confirmation restates what will be destroyed.
3. **Walk away.**  The job overwrites every sector, reads every sector back, compares SMART before and after, and, for Wipe + Load, lays down an Ark and fills the rest.
4. **Look it up later.**  Every drive lands in a searchable list of serial numbers.  Click one to see its record.  Print it, or save it as a PDF to email with the drive.

There's no login, and the page isn't reachable from outside the LAN.  The record leaves the house on paper or as a PDF attachment, never as a link.

### Goals, in order

1. **Can't be pointed at the wrong disk.**  This outranks everything else.
2. **Prove the wipe.**  Write every sector, read every sector back, and say exactly what happened.
3. **Keep a record per serial** that you can still read when the software is gone.
4. **Load an Ark** and fill the drive to the last byte.
5. **Survive real life:** the browser closes, the VM reboots, someone yanks the drive.

### Non-goals

- More than one bay.  A second caddy would be a second instance.
- Access from the internet, or any kind of public lookup.
- Compliance.  The record states what this machine did.  It isn't an attestation anyone else has to accept.

## 2. Decisions (xram, 2026-10-23)

| Question | Call |
|---|---|
| Who sees it | Local only, at `zawiper.example.org`.  The record ships as print or PDF, matched to the serial. |
| Confirmation | No typed serial, no hold-to-arm, no countdown.  Show the filesystems found and how much file data they hold, then one deliberate confirm. |
| On insert | Read-only facts load automatically.  The button is Re-read. |
| Jobs | **Wipe**, and **Wipe + Load** (an Ark loadout). |
| After | Searchable list of serials → click → results. |
| Storage | Files per drive (§7).  The list is built by scanning them, so no database is needed at this scale. |
| Test drive | The Seagate in the bay now (`9VPAX9NA`) is off the wipe pile and can be erased. |

## 3. Hardware: the facts (Val, Phase 0)

- **The passthrough is live.**  dev-host → `dock-host` (VMID 102), `usb0: host=4-2`, attached by port.
- **One USB device, two slots** (ASMedia ASM1156-PM bridge).  A swap in either slot resets both.  Nothing the host sees names a physical slot.  **v0.2 is one drive at a time:** `bayd` refuses to act if it sees more than one LUN.
- **Slot 0 once reported a placeholder size** (4.14 GB for two different drives).  Refuse a drive whose block size doesn't match what SMART reports, or whose SMART can't be read.
- **`smartctl -d sat` works from inside the VM.**
- **Key everything on the SMART serial.**  The kernel's serial was all zeros with two drives in the enclosure.
- **Still unmeasured:** ATA security erase through this bridge, sustained write and verify throughput, and a udev rule that authorizes storage interfaces only on that port.  Phase 1 measures the first two on the test drive.

## 4. Safety

The visible part is one confirm.  The real guards are the ones that never show up on the page.

### 4.1 Invisible, always on

- **The API can't name a disk.**  No endpoint takes a device path, a serial, or a major:minor number.  `bayd` finds the bay itself from `/etc/zawiper/bay.toml`, which pins it to a USB port path and is written once by `bayd provision` (*v0.1 §4.1, §5.1*).
- **One LUN, sane size.**  `bayd` refuses if the port shows two block devices, if the size doesn't match SMART, or if the size is outside 8 GiB–64 TiB.
- **Never a host disk.**  `bayd` refuses anything mounted by the host, or anything holding a root, Docker, or LVM/ZFS member that the VM is using.  The VM's own disks are `sd*`/`vd*` too, which is exactly what `ark stamp`'s current guard gets wrong (§9).
- **Identity is pinned for the whole job.**  Before each destructive phase, and at least every 100 GiB, `bayd` re-reads serial, size, and WWN.  Any change aborts the job and raises **ALARM**.
- **Fail closed.**  On a write error, read mismatch, or vanished device, the job ends **FAILED** with the byte offset, and nothing continues.  A job that was running when the VM went down comes back **INTERRUPTED**.  Nothing resumes by itself, and nothing is ever reported done unless it was proven done.

### 4.2 Visible: the confirm

> **Erase Seagate ST31000528AS, serial 9VPAX9NA, 1.00 TB?**
> Found on it: GPT · 2 partitions · ext4 `data` (312 GB of files) · NTFS `Windows` (88 GB of files).
> [ Cancel ]   [ Erase this drive ]

- **"Found on it" comes from a read-only peek.**  It lists the signature scan, then mounts each recognized filesystem `ro,noexec,nosuid,nodev` (plus `noload` for ext4) in a throwaway mount namespace, measures used space, and unmounts.
- **"Nothing found" is said plainly:** "No partition table, no filesystems.  Looks blank already."
- **Backup drives get flagged.**  A label starting with `backup-`, a restic repository, or a PBS datastore turns the card amber, and the confirm button reads **Erase this backup drive**.  The rotation drives use the same caddies, so this costs nothing and catches the 2 a.m. mistake.
- **The confirm resets if the drive's facts change** while it's on screen.

### 4.3 The page itself

- **Network:** the Caddy route on hub-host uses the private-ranges guard and the WAF import (Val).
- **Browser:** each mutating call needs a matching `Host` and `Origin`, `Content-Type: application/json` (which forces a CORS preflight the server refuses), and an `X-Za-Wiper: 1` header.  A malicious page open in a LAN browser can't start a wipe (*v0.1 §4.5*).
- **One job at a time.**

## 5. States

```
EMPTY ─insert─► READING ─► PRESENT ─confirm─► RUNNING ─► DONE
  ▲                ▲          │                  │ ├──► FAILED
  │                └─Re-read──┘                  │ └──► ALARM (identity changed)
  │                                              └ VM/bayd died ─► INTERRUPTED
  └──────────── remove / Eject (from any settled state) ◄───────────
```

| State | The page says |
|---|---|
| `EMPTY` | "Nothing in the bay." Search box, last few drives. |
| `READING` | The command being run right now. |
| `PRESENT` | Drive card, Re-read, Wipe, Wipe + Load. |
| `RUNNING` | **Do not pull the drive.**  Phase, progress, MB/s, ETA, temperature, errors. |
| `DONE` | Grade, headline numbers, link to the record, Eject. |
| `FAILED` | The evidence first: phase, offset, what the drive said. |
| `INTERRUPTED` | "The power went out mid-wipe.  I don't know what state this drive is in, and I won't pretend I do."  Stays until acknowledged, then logs an incomplete job. |
| `ALARM` | Full-width stop.  Job aborted.  Needs acknowledgement. |

State is written to disk before every transition.

## 6. Jobs

### 6.1 On insert: read (no writes, ever)

Identity, SMART attributes and logs, the signature scan, and the read-only peek (§4.2).  This snapshot is saved as an `inspect` record, so a drive that's only ever been looked at is still in the list.

### 6.2 Wipe

| # | Phase | Notes |
|---|---|---|
| 1 | SMART before | Snapshot. |
| 2 | Firmware erase | ATA Secure Erase or `blkdiscard`, if the bridge passes it.  If it fails, that's noted, not an error; the overwrite still runs. |
| 3 | **Overwrite** | Every sector, with a seeded keystream (*v0.1 §8.1*: ChaCha20 or AES-CTR, random key per job, aligned blocks ≥ 4 MiB, `O_DIRECT`).  Throughput sampled every few seconds. |
| 4 | **Verify** | Regenerate the keystream, read every sector back cold, and compare.  This is also a capacity test: a drive that lies about its size fails at the point where it wraps. |
| 5 | SMART after | Diff of reallocated, pending, uncorrectable, CRC and temperature. |
| 6 | Clear signatures | Zap GPT and MBR.  The drive is left blank. |
| 7 | Record | §7. |

### 6.3 Wipe + Load

Phases 1–5 of Wipe, then:

| # | Phase | Notes |
|---|---|---|
| 6 | Partition and format | exFAT, label `THE-ARK`. |
| 7 | **Load** | Copy the chosen loadout from staging, plus `--pack=` if one is picked.  Preflight first: does it fit?  The page only offers loadouts that fit the drive. |
| 8 | **Verify the load** | Unmount, remount, re-read every file, and compare its SHA-256 with staging. |
| 9 | Records on the drive | `ZA-WIPER.txt` (the human copy, with the viper on top) and `ZA-WIPER.json`. |
| 10 | **Ballast** | Keystream into `DELETE_THIS_IF_YOU_NEED_SPACE` until `ENOSPC`, then check that a 1-byte write fails too.  The final record goes into a 16 MiB reserve freed at the end (*v0.1 §8.2–8.3*). |
| 11 | Sync, unmount, eject | |

The payload source is the staging symlink farm, so the copy follows links (`--copy-links` semantics).  It copies **what the manifest names**, including held builds like the Wikisource ones, which is the opposite of today's `ark stamp` filter (§9).

### 6.4 Grade

| Grade | When |
|---|---|
| **PASS** | No mismatches, no read errors, no SMART counts rising, no stalls. |
| **PASS WITH NOTES** | Counts that are non-zero but stable; a few stalls; high temperature; a SMART attribute that tripped in the past.  The test drive is this case: airflow temperature `In_the_past`. |
| **FAIL — DO NOT USE** | Any mismatch or read error, SMART counts rising during the job, or a size or identity inconsistency.  A FAIL drive can't be loaded. |

Thresholds live in a config file, with the reason next to each one.

## 7. The record

**Files are the source of truth.**  Each drive gets a folder named for its SMART serial.  Each job adds one record.

```
/srv/zawiper/drives/
  9VPAX9NA/
    drive.json                      # latest identity, for the list
    2026-10-23T14-02-11Z-inspect/
      record.json                   # canonical
      record.md                     # human, rendered from record.json
      smart.txt                     # raw smartctl -x
    2026-10-24T09-15-40Z-wipe/
      record.json
      record.md
      smart-before.txt
      smart-after.txt
      throughput.csv                # t, offset, MB/s
```

- **The list is built by scanning** `drives/*/drive.json` at startup and after each job.  A homelab bay will see hundreds of drives, not millions.  If that ever gets slow, a SQLite index can be rebuilt from the files whenever it's needed, but it's never the source of truth.
- **`record.md` is the record a person reads.**  It reads fine in a terminal, in `less`, or on the drive 20 years from now.
- **Print and PDF** are rendered from the same data.  The page has a print stylesheet, and the PDF is produced server-side so it can be attached to an email.  Each one carries the serial in large type at the top.
- **Each record holds:** the drive's identity (and which fields the bridge hid), the job and its options, per-phase results with tool versions and byte counts, verify results, the SMART before/after diff, a throughput summary and stall list, the grade and the rule that produced it, loadout and verify results for a Load job, keystream parameters so anyone can re-verify, and Za Wiper's version and git SHA.
- **Tamper evidence** comes later (Phase 3): `record.json` signed with `ssh-keygen -Y sign` (*v0.1 §9.4*).
- **Backups:** `/srv/zawiper/` is the one thing on `dock-host` that can't be recreated.  It goes in the backup scope the day it ships (Val).

## 8. Architecture

- **`bayd`**: Go, root, systemd, on `dock-host`.  Owns the block device, mounts, the keystream writer and reader, and the records.  It listens on a unix socket with a fixed verb list: `status`, `reread`, `wipe`, `load`, `abort`, `ack`, `eject`, `records`.  No verb takes a device, and there's no "run this" verb.
- **`zaweb`**: Go, an unprivileged container with a read-only root filesystem, no devices, and the socket bind-mounted in.  It serves the embedded UI, server-sent events for live progress, the list, and the records.  A bug in `zaweb` can't write to a disk.
- **Payload:** staging mounted read-only on `dock-host` over NFS.  The NAS export and firewall need `dock-host` added (Val).
- **Tests without a drive:** `bayd --bay-file` on a loop device, with device-mapper `error` and `delay` targets for read failures and stalls, and a wrap-around map that fakes a lying-capacity drive (*v0.1 §13*).
- **Code lives in** `zawiper/` in this repo.  It deploys through its own Woodpecker lane; the seed pipeline doesn't run for changes there.
- **ntfy** on DONE, FAILED, INTERRUPTED, and ALARM.  A 20-hour job ends while you're asleep.

## 9. What this means for `ark stamp`

Za Wiper replaces `stamp` for drives that go through the bay, and the bugs v0.1 passed along (*v0.1 §15*) are ours now:

1. **Its device guard treats every `/dev/sd*` as USB.**  Inside a VM that's false.  The port-pinned bay replaces the guard.
2. **`dd … || true` swallows a failed wipe** and prints "wipe complete" anyway.
3. **The sudo helper passes `mount`/`chown` arguments through unfiltered.**
4. **Nothing re-reads the drive after the clone.**
5. **It filters ZIMs by the catalog's newest filename**, which leaves held builds off the drive.

`stamp` stays usable at a shell.  Items 2 and 5 get fixed there too, since they're one-line bugs with real consequences.

## 10. Phases

| Phase | Deliverable | Needs |
|---|---|---|
| **1 — Read-only bay** | `bayd` read path, `zaweb` drive card, Re-read, inspect records, serial list, record page with print.  Nothing can write. | Val: port, Caddy route, DNS |
| **2 — Wipe** | Keystream overwrite + verify, SMART diff, grade, `record.md`, PDF.  First real run on `9VPAX9NA`, which also measures throughput and ATA erase through the bridge. | |
| **3 — Wipe + Load** | Format, load, verify the load, ballast, reserve, on-drive records.  Signing. | NFS export to `dock-host` |
| **4 — Polish** | ntfy, metrics, a backup-drive profile, a destruction note on a record. | |

## 11. Open

- **Val:** the `zaweb` port (to come), the Caddy route `zawiper.example.org` → `192.0.2.18`, NFS export for staging, `/srv/zawiper` in backup scope, and a Woodpecker deploy lane.
- **Zim:** the seed pipeline stages whatever the catalog has newest, so a hold in a manifest is only a comment to it.  Holds need to be real (a pinned `file:` in the manifest) before the next push that should run seed.

---

3-2-1-1-0.  The last 0 means a drive proven blank, and Za Wiper is how we prove it.
