# Za Wiper — drive bays that read, wipe, prove, and sometimes fill with an Ark

Spec v0.3.1, 2026-10-24.  Zim, for The Ark.  Idea and name: xram.

v0.1 was written 2026-10-09 in the homelab repo (`docs/specs/za-wiper.md` at `d73b3bc`).  v0.2 moved it home and folded in xram's calls of 2026-10-23 and Val's Phase 0 hardware facts.  v0.3 adds the calls of 2026-10-24:
- many bays on many hosts behind one hub
- the wipe record as a certificate for resale
- one shared view, with no abort
- one `bayd` per bay, with its scope enforced by the kernel (relayed by Val, with the hub and CA plan)

The first real wipe ran that day.  Where v0.1 had more detail than this, and that detail still holds, this doc says so by section number, e.g. *v0.1 §8.3*.

## 1. What it is

Drive docks on any host assigned to the job, each run by a `bayd`, all reporting to one hub.  A local-only web page at **`zawiper.example.org`** shows every bay on one wall.  The first dock is a USB caddy on dev-host, passed through by port to `dock-host` (192.0.2.18).

**Mostly, it wipes drives for sale.**  Buyers on eBay and r/homelabsales want three things: a blank drive, proof nothing's left on it, and that drive's own health.  The record is that proof, and it prints as a certificate (§7).  Loading an Ark is the rarer job, for drives given as gifts or handed out.

1. **Put a drive in.**  Its read-only facts appear on their own: identity, SMART, the filesystems on it, and how much file data they hold.  The button on the page is **Re-read**.
2. **Look, then decide.**  If it's okay to wipe, press **Wipe** or **Wipe + Load**.  The confirmation restates what will be destroyed.
3. **Walk away.**  The job overwrites every sector, reads every sector back, compares SMART before and after, and, for Wipe + Load, lays down an Ark and fills the rest.
4. **Look it up later.**  Every drive lands in a searchable list of serial numbers.  Click one to see its record.  Print it, or save it as a PDF to email with the drive.

There's no login, and the page isn't reachable from outside the LAN.  The record leaves the house on paper or as a PDF attachment, never as a link.

### Goals, in order

1. **Can't be pointed at the wrong disk.**  This outranks everything else.
2. **Prove the wipe, to a stranger.**  Write every sector, read every sector back, and say exactly what happened, in a certificate a buyer can check without trusting us (§7).
3. **Keep a record per serial** that you can still read when the software is gone.
4. **Load an Ark** and fill the drive to the last byte.
5. **Survive real life:** the browser closes, the VM reboots, someone yanks the drive.

### Non-goals

- Access from the internet, or any kind of public lookup.
- A file browser.  The disk side has no network and the hub has no device, so drive contents never cross the link.  Records never hold what was on a drive either.  If you want something off a drive, eject it and mount it on a workstation read-only.  The inspect page shows the partitions, labels and used space, and a wipe needs the serial typed in, so you get a last look before anything is erased (xram, 2026-10-25).
- Being someone else's audit.  But the target is to **satisfy NIST SP 800-88 Rev. 2** (xram, 2026-10-24).  Rev. 2 replaced Rev. 1 in September 2025.  Apart from crypto erase, it no longer lists techniques itself; it defers to IEEE 2883-2022.  So, for each media type:
  - **Purge where the drive allows it.**  That means the drive's own sanitize command (overwrite, block erase, or crypto erase), from IEEE 2883's table for that interface.
  - **Clear otherwise.**  That's our keystream overwrite with full read-back.  It can't reach sectors the drive has remapped.
  - **Every sanitization is verified,** and every certificate names the level, the technique, and the standard clause.
  - **Flash only ever gets firmware sanitize.**  An overwrite proves nothing on an SSD.
  - We never claim Purge when the command wasn't available or didn't get through the bridge.
  - 9VPAX9NA (ATA8-ACS, which predates SANITIZE) offers only ATA Security Erase.  IEEE 2883 counts it as Purge in Enhanced Erase mode (§8.4.3.2.1) and as Clear in Normal mode (§8.4.2.3), and recommends neither without the manufacturer confirming the model's implementation.  The ASM1156 bridge passes it (2026-10-25, §11).

## 2. Decisions (xram, 2026-10-23 and 24)

| Question | Call |
|---|---|
| Who sees it | Local only, at `zawiper.example.org`.  The record ships as print or PDF, matched to the serial. |
| Confirmation | No typed serial, no hold-to-arm, no countdown.  Show the filesystems found and how much file data they hold, then one deliberate confirm. |
| On insert | Read-only facts load automatically.  The button is Re-read. |
| Jobs | **Wipe**, and **Wipe + Load** (an Ark loadout). |
| After | Searchable list of serials → click → results. |
| Storage | Files per drive (§7).  The list is built by scanning them, so no database is needed at this scale. |
| Test drive | The Seagate in the bay now (`9VPAX9NA`) is off the wipe pile and can be erased. |
| While it runs | **One shared view.**  Everyone who opens the page sees the same thing, and nothing on it belongs to anyone's session. |
| Stopping a run | **No abort on the page.**  Once a run starts, nobody watching can break it.  We all just get to watch. |
| The look | The wipe screen is MS-DOS DEFRAG: a grid of blocks being wiped, then verified, then loaded (§6.5). |
| What it's for | Mostly wiping drives for sale.  The buyer wants a blank drive, a certificate of deletion, and that drive's health.  Wipe + Load is the gift case. |
| Many bays | One web interface, many bays, like OSDs in Ceph.  Each bay checks in and can take disks.  One hub (`zaweb` plus the records) talks to all of them.  A drive's history follows its identity, not the dock it was in (§8). |
| Scope | A USB-SATA dock, a USB-SAS adapter, and a port on a SAS card all work the same way.  Each bay's service is scoped to one device, by USB port or hardware ID.  What's in its config is what it can touch, and nothing else. |
| Who it's for | Two deployments of one codebase.  The homelab: one operator, no login, drives for sale.  **the reference institution**: the institutional reference deployment.  That means OIDC sign-in (Keycloak, with Globus Auth brokered through it; fauxbus stubs Globus for testing), operator and viewer roles, custody fields (asset tag, ticket, owner, disposition), hash-chained records, and destruction records.  The code stays brand-free; each deployment's identity, roles and certificate letterhead live in its own config. |
| Load payload | The hub serves staging, and `bayd` pulls it over plain HTTP with range requests.  No tusd, and no NAS mount on dock hosts.  Sending a loadout over the network twice is fine. |

## 3. Hardware: the facts (Val, Phase 0)

- **The passthrough is live.**  dev-host → `dock-host` (VMID 102), `usb0: host=4-2`, attached by port.
- **USB passthrough can't take a swap (2026-10-25).**  The dock re-enumerates on every insert and pull.  The first time it did, QEMU's libusb host passthrough hit `usb_host_ep_update: Assertion 'alt < …num_altsetting' failed` and the VM died with it.  On the reboot the guest named the port `5-1`, not `10-1`.  A dock behind a VM needs a whole USB controller passed through (PCIe), so the guest kernel sees the hotplug; or the dock goes on bare metal.
- **One USB device, two slots** (ASMedia ASM1156-PM bridge).  A swap in either slot resets both.  Nothing the host sees names a physical slot.  **One drive per bay:** `bayd` refuses to act if it sees more than one LUN.  `usb_lun` makes each slot its own bay (§8).
- **Slot 0 once reported a placeholder size** (4.14 GB for two different drives).  Refuse a drive whose block size doesn't match what SMART reports, or whose SMART can't be read.
- **`smartctl -d sat` works from inside the VM.**
- **Key everything on the SMART serial.**  The kernel's serial was all zeros with two drives in the enclosure.
- **ATA Security Erase gets through this bridge** (§11).  Still unmeasured: sustained write and verify throughput, and a udev rule that authorizes storage interfaces only on that port.

## 4. Safety

The visible part is one confirm.  The real guards are the ones that never show up on the page.

### 4.1 Invisible, always on

- **The API can't name a disk.**  No endpoint takes a device path or a major:minor number.  Each bay's selector lives in `/etc/zawiper/bays/<bay>.toml`, written once by a person provisioning the host (*v0.1 §4.1, §5.1*).  The selector is a USB port path, a SAS by-path (`pci-…-sas-phyN-lun-0`), or an enclosure slot.  The hub can address a bay, never a device.
- **The kernel enforces the scope, not `bayd`'s code.**  A udev rule matches the selector and creates `/dev/zawiper/<bay>`, plus `/dev/zawiper/<bay>-sg` for SAS tools.  The bay's disk unit runs with `DevicePolicy=closed` and `DeviceAllow=` those nodes only, so even as root the cgroup device filter refuses any other disk (§8).
- **RAID cards only in HBA/IT mode.**  Behind `megaraid`, SMART and passthrough go through one controller-wide ioctl node that can reach every disk on the card.  That breaks the scope, so the bay refuses it.
- **A start carries what the confirm showed.**  Wipe and Load name the bay, the drive identity, and the insertion session the confirm was drawn from.  `bayd` refuses if that isn't what it holds right now.  So a stale confirm, a swapped drive, or a hub that's confused all fail closed.
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
- **Dock hosts don't listen.**  Each bay dials out to the hub over TLS, with its own client certificate (CN `bay/<host>/<name>`, like an OSD's keyring), and keeps that one connection open.  Commands come down it, and status and records go up.  A dock host exposes no port.  The hub is the trust point: owning it means being able to wipe whatever is sitting in a bay.  So it stays small, on a backed-up host.
- **Browser:** each mutating call needs a matching `Host` and `Origin`, `Content-Type: application/json` (which forces a CORS preflight the server refuses), and an `X-Za-Wiper: 1` header.  A malicious page open in a LAN browser can't start a wipe (*v0.1 §4.5*).
- **One job at a time.**
- **One shared view.**  The page draws only what `bayd` reports.  There's no per-visitor state, no "your job", and no control that exists for one viewer and not another.  Ten people watching see the same frame.  The confirm dialog belongs to whoever is about to start a run.  Once they press it, the run belongs to the bay.
- **Nothing on the page stops a run.**  `bayd` stops a job only on its own evidence (§4.1: identity changed, write error, mismatch).  The only other stop is a person with root on `dock-host`, at a shell: `systemctl stop bayd`.  The job then comes back **INTERRUPTED**, like a power cut.

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
| `RUNNING` | **Do not pull the drive.**  The surface map (§6.5), phase, MB/s, ETA, temperature, errors.  No buttons. |
| `DONE` | Grade, headline numbers, link to the record, Eject. |
| `FAILED` | The evidence first: phase, offset, what the drive said. |
| `INTERRUPTED` | "The power went out mid-wipe.  I don't know what state this drive is in, and I won't pretend I do."  Stays until acknowledged, then logs an incomplete job. |
| `ALARM` | Full-width stop.  `bayd` ended the job itself.  Needs acknowledgement. |

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
| 6 | Record | §7.  The keystream is left on the drive as it is.  It carries no partition table or filesystem signature, so the drive reads as blank.  Leaving it untouched is what lets a buyer re-check it. |

### 6.3 Wipe + Load

Phases 1–5 of Wipe, then the load.  The payload comes from the hub, not from a NAS mount on the dock host:
- **The hub serves staging read-only over HTTP,** following the symlink farm.  It holds the NAS mount; dock hosts never see the NAS.
- **First comes the loadout's list:** files, sizes, and each ZIM's SHA-256 from Kiwix's `.meta4`.  `bayd` checks that it fits before writing anything.
- **`bayd` pulls each file onto the target drive.**  A dropped connection resumes with a range request from the partial file's length.  The partial file is the bookmark, so nothing is staged twice.
- **The load is checked against Kiwix's hashes,** not against the transfer.  So one check catches a bad link, a bad cable, and a bad sector alike.
- **On cost:** every byte crosses the network twice, NAS → hub → dock.  That's accepted: one bay over gigabit runs about as fast as a drive writes, and loads are rare.

| # | Phase | Notes |
|---|---|---|
| 6 | Partition and format | exFAT, label `THE-ARK`. |
| 7 | **Load** | Pull the chosen loadout from the hub, plus `--pack=` if one is picked.  The page only offers loadouts that fit the drive. |
| 8 | **Verify the load** | Unmount, remount, re-read every file, and compare its SHA-256 with Kiwix's. |
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

### 6.5 The surface map

The wipe screen is DEFRAG.  The drive is a grid of 1 GiB blocks.  It's 932 cells for a terabyte, laid out in text-mode rows and drawn in the IBM VGA 8×16 font (Ultimate Oldschool PC Font Pack, CC BY-SA 4.0, self-hosted).  The colors are the blue field and gray frames, with a status box and a legend below.

| Wall | Printout | Means |
|---|---|---|
| `░` | `░` | not reached yet |
| `W` / `r` | `W` / `R` | being written / being read back now, blinking, as DEFRAG had them |
| cyan `■` | `▓` | written |
| white `■` | `█` | read back and verified |
| yellow `■` | `▒` | slow: over twice the median time of its 16 neighbours.  Neighbours, because a spinning drive slows toward the inner tracks on its own. |
| `B` | `B` | bad: a write or read error, or bytes that came back wrong |
| shelf colors | | Load: each ZIM lands as a run, colored by shelf.  The cells come from where the filesystem actually put the file, not from a cursor. |

The wall draws discrete squares, so the grid reads as blocks rather than a fill; the printout has no color, so it keeps the shades.  Past 1,600 blocks the wall groups them (a 4 TB drive is 3 GiB a cell) and says so under the legend.

A 1 GiB block takes seven seconds or so, so the map alone moves in steps.  Between blocks, the engine reports after every 8 MiB buffer, and the link's once-a-second poll carries it: the written and verified byte counts, the percentage and the clock all move every second.  Under the boxes, a **Throughput** box graphs each block's MB/s across the drive, outer edge first: cyan as written, white as read back, so the read-back visibly catches up.  It's the drive's own speed curve, falling toward the inner tracks, in half-block characters.

`bayd` keeps the cell states, and the page gets them as one short string a tick.  Every viewer gets the same string (§4.3).  The final map, with each block's write and read times, goes into the record.  `record.md` and the printout carry it as text, so the map ships with the drive.

The first map came from `zwipe` (`zawiper/cmd/zwipe`), the hand-run wipe for `9VPAX9NA` on 2026-10-24.  It's a root CLI with the same read and refusals as `bayd`, and the serial on the command line as the confirmation.  Phase 2 moved its engine into `bayd`, and the CLI is gone.

## 7. The record

**Files are the source of truth, and they live on the hub.**  Each drive gets a folder.  Each job adds one record, from whichever bay ran it.

- **Folder names use the drive's identity, not just its serial.**  Two vendors can ship the same serial.  The folder is the serial when it's unique, and serial plus a short WWN or model suffix when it isn't.  `drive.json` holds the full identity, and search finds a drive by serial, WWN, or model.
- **`bayd` keeps a finished record** on its own disk until the hub confirms it's stored.  If the hub is down when a wipe ends, nothing is lost.

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
- **Each record holds:**
  - the drive's identity, and which fields the bridge hid
  - the bay and host
  - the job and its options
  - per-phase results, with tool versions and byte counts
  - verify results
  - the SMART before/after diff
  - per-block timings (the surface map)
  - the grade, and the rule that produced it
  - loadout and verify results, for a Load job
  - the keystream parameters
  - Za Wiper's version and git SHA
- **The keystream key is published, on purpose.**  A buyer can regenerate the stream and read the drive back against it, so they can confirm the wipe without trusting us.  Publishing the key loses nothing: the old data is gone whether or not the noise can be reproduced.  `zwipe` 2026-10-24 kept its key in memory only.  That's the one thing this changes in the engine.
- **A wipe record prints as a certificate of sanitization.**  It's laid out after NIST SP 800-88's sample certificate:
  - drive identity
  - method (*Clear*: one keystream overwrite, fully read back)
  - verification result
  - date, tool and version
  - health before and after
  - the surface map
  - the signature

  A cropped surface map with the health headline also makes the listing photo.
- **The sheet travels with the drive (xram, 2026-10-25).**  When a wipe finishes, the hub sends the certificate to a color printer, unattended.  It's folded in quarters and taped to the outside of the static bag, so it's laid out for the fold:
  - **The outside quarter is the label:** serial in large type, model and size, the verdict, the grade, the date, and "Signed proof on request" with the record ID.  It reads through tape without unfolding.  A hub given a public `-site-url` prints a QR code to the record there instead; at home there isn't one.
  - **The inside is the certificate,** with the surface map in color.
  - **A failed drive prints too,** as a red "not for sale" sheet, so it can't drift back into the sell pile.
  - **The same render saves as a PNG** for the listing: one image per drive, ready to post or give away.
- **Lots.**  Most of the pile is 1 TB, with some 2 TB and a few 4 TB, so they sell best as lots of two or three matched drives.  Three is a mirror and a spare.  A lot is a named group of serials: same model, close in power-on hours.  It gets its own page and one PNG with every drive's surface map side by side.  The lot is graded by its worst drive.
- **Signed, from Phase 2.**  `record.json` is signed with `ssh-keygen -Y sign` (*v0.1 §9.4*), and the public key and the verification steps are printed on the certificate.  An unsigned certificate is just a nice PDF.
- **A verifier for buyers:** a short script, or a page on the certificate, that reads a drive and checks it against the published key.
- **Backups:** the hub's `/srv/zawiper/` is the one thing in the system that can't be recreated.  It goes in the backup scope the day it ships (Val).

## 8. Architecture

```
   dock hosts (any number)                      the hub
 ┌────────────────────────┐
 │ bay A, bay B           │──┐        ┌────────────────────────────┐
 └────────────────────────┘  │  TLS   │ zaweb                      │
 ┌────────────────────────┐  ├───────►│  · the wall of bays        │
 │ bay C                  │──┤ dial   │  · records, by drive       │
 └────────────────────────┘  │ out    │  · staging, read-only      │
 ┌────────────────────────┐  │        └────────────────────────────┘
 │ bay D (SAS HBA port)   │──┘
 └────────────────────────┘
```

- **`bayd`, one per bay**, the way Ceph runs one OSD per disk.  Go, a static binary, systemd, no Docker.  Each bay is two template units, with the network side and the disk side kept apart:
  - **`bayd-link@<bay>`** is always running.  It holds the TLS connection, the heartbeat, and the spool of finished records.  For Load, it pulls the payload from the hub.  It has no device access at all (`DevicePolicy=closed`, nothing allowed), so the hub, and anyone who owns the hub, reaches the disk only through the next unit's checks.
  - **`bayd@<bay>`** is the disk side.  udev starts it when the selector's device appears, and it's bound to that device (`BindsTo=`), so it stops when the drive leaves.  `DeviceAllow=` is resolved fresh on every insert, which sidesteps a re-inserted drive getting a new minor number.  It has no network (`IPAddressDeny=any`, unix sockets only).  It owns the read, the keystream writer and reader, the mounts, and the job, and it talks to its link over a unix socket in `/run/zawiper/<bay>/`.
  - A running job needs nothing from the link or the hub.  Either can restart without touching it.  If the drive leaves mid-job, the link reports **INTERRUPTED** from the job's state file.
  - The wall shows a bay as empty while its link is up and its disk unit isn't running, and as offline when its link is down.
  - `/etc/zawiper/bays/<bay>.toml` holds the selector, the SMART type (`sat` or `scsi`), and what the bay can do: `wipe`, `load`, `sas`.  A two-slot USB dock is one bay by default: its port is the selector, and the one-LUN refusal stays.  `usb_lun = "0"` and `usb_lun = "1"` split it into a bay per slot.  A swap in either slot resets both, and systemd stops the other slot's `bayd@` with its device, so a wipe running there ends INTERRUPTED and never gets a record.  Swap only when both bays are idle.
  - Commands, from the hub: `status`, `reread`, `wipe`, `load`, `ack`, `eject`.  No command takes a device, there's no "run this" command, and there's no abort (§4.3).
  - The host can be bare metal or a VM with the dock passed through.  The VM is preferred: a mistake can't reach a hypervisor's own disks.
  - **Releases don't go through CI.**  A key that can install `bayd` can wipe whatever is in a bay.  So `bayd` ships through a `fleet zawiper-bay <host>` play run with xram's become: a pinned binary, plus each bay's config and certificate.  Nothing installs it automatically.
- **The hub, `zaweb`**: Go, unprivileged, with no devices.  It deploys through the-ark's Woodpecker lane like any other container.  It runs as a stack on hub-host (§11).
  - It holds the `bayd` connections, the records, and a read-only staging mount for Load.
  - It serves the wall of bays (one shared view, §4.3), the list, the records, and the certificates.  Live progress reaches browsers as server-sent events.
  - A bug in it can't write to a disk directly.  It can only send `bayd` a command, and `bayd` checks every one (§4.1).
- **Payload:** pulled from the hub (§6.3).  Dock hosts need no NFS export.
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
| **2 — Wipe, networked** | The hub/`bayd` split with dial-out TLS, one `bayd` per bay.  The kernel enforces each bay's scope.  Records move to the hub.  The wipe in `bayd`, the live surface map on one shared wall, the SMART diff and the grade.  Signed certificates, with a published key and a buyer's verifier.  **Built 2026-10-25** and run end to end on a file: hub, link and `bayd` over mTLS, wipe, record to the hub, signed on arrival, checked by `zverify` and `ssh-keygen -Y verify`.  Still to prove on hardware: the units and the device filter on dock-host, then a real drive.  Still to measure: ATA erase through the bridge. | Val: the CA and certs, the Caddy route to `zaweb:8460`, `/opt/zawiper/tls` |
| **3 — Wipe + Load** | The hub serves staging; `bayd` pulls with range requests.  Format, load, verify against Kiwix's hashes, ballast, reserve, on-drive records. | Staging mount on the hub |
| **4 — Polish** | ntfy, metrics, a backup-drive profile, a destruction note on a record. | |

## 11. Open

- **Val:**
  - the Caddy route `zawiper.example.org` → `192.0.2.18:8460`
  - `/srv/zawiper` in backup scope
  - a Woodpecker deploy lane
  - **Done 2026-10-24:** the route `https://zawiper.example.org` → `192.0.2.18:8460`, private ranges only, with the WAF; the homelab spec is now a pointer to this one.
  - **Done 2026-10-25:** the route now goes to `zaweb:8460` on the gateway network (homelab c608965).  The migration left it pointing at dock-host, which 502ed.
  - **Hub placement, decided (xram, 2026-10-24):** a stack on hub-host.  It moves in Phase 2, when the hub connection replaces the local socket.
    - Its `/opt/<stack>/data` is already in the hourly restic job.
    - `/mnt/nas` is already mounted, so staging needs no new export.
    - Caddy is local.
    - The cost: hub-host is the most sensitive host, and Load traffic runs NAS → hub-host → dock.  Accepted.
  - **The CA:** an offline root made with the `step` CLI, with no step-ca server.  The root key stays with the DR keys.  The hub gets a server certificate, and each bay a one-year client certificate.  Val mints them once the bay names are settled.
  - **Deploys:** the Woodpecker lane covers the hub.  `bayd` ships by fleet play (§8).
  - Dock hosts need no NFS export.
- **NIST 800-88 Rev. 2 / IEEE 2883:** xram has a copy of IEEE 2883-2022 (2026-10-25; kept out of the repo, it's copyrighted).  What it settles:
  - **Clear for ATA (§8.4.2.2)** first resets anything that keeps sectors from the host: HPA, DCO, Accessible Max Address, zone activation, depopulation, TCG locking ranges.  Then it writes every block up to the native maximum.  "Multiple passes or more complex values can be used", so a keystream qualifies.  The bay now checks HPA and DCO with `hdparm` and refuses a drive that hides sectors.  A record without that check claims no level, which includes the Phase 1 wipe of 9VPAX9NA.
  - **Clear for SCSI (§8.4.2.4)** resets the MODE block descriptor's block count first.  That isn't checked yet, so SAS drives claim no level until it is.
  - **Purge for ATA (§8.4.3.2)** is crypto scramble, BLOCK ERASE EXT, OVERWRITE EXT, or Security Erase Unit in Enhanced mode.  **Purge for SCSI (§8.4.3.3)** is SANITIZE only; FORMAT UNIT is Clear.
  - **Verification (§7):** for Clear, sampling is enough (§7.3: at least 5% at random, or two random blocks in each of 10,000 sections plus the first and last).  For Purge, full verification (§7.2).  Our full read-back covers both.  zverify's default now samples per §7.3.
  - **Still open:** map each bay type to the sanitize commands it actually passes through.  The Accessible Max Address, zone and depopulation checks for newer drives.
  - **ATA Security Erase through the ASM1156, tested 2026-10-25** on 9VPAX9NA, a drive we could afford to brick.  By hand, outside bayd:
    1. 65 one-MiB canaries written across the disk.
    2. `hdparm --security-set-pass`, then `--security-erase-enhanced` as a transient systemd unit.
    3. 161 min 28 s, against the drive's own 164-minute estimate.  Exit 0, no USB resets.
    - Every canary is gone.  The drive wrote a vendor pattern, `33 cc 55 aa` repeated, start to end.  Security came back "not enabled", with no password left behind.
    - The drive has no SANITIZE feature set, so Enhanced Security Erase is its only Purge.  It reaches remapped sectors, which our overwrite can't.  Its pattern is fixed and known, so a full read can verify it (§7.2).
  - **Still open for a Purge job in bayd:**
    - What the full read checks.  It could look for that drive's fixed pattern, or check that none of a set of pre-erase canaries survived.
    - How a record cites the manufacturer's confirmation that §8.4.3.2.1 asks for.
    - What the bay does if the bridge drops the command mid-erase.  The drive then stays locked with a password bayd knows, so bayd can unlock it and disable security.
- **SAS:** a USB-to-SAS adapter versus an LSI HBA in IT mode with SFF-8482 breakouts.  xram has a PCIe RAID card to try.  It qualifies only if it runs as a plain HBA (IT firmware, or a mode that loads `mpt3sas`/`mpt2sas`, not `megaraid_sas`).  It can go straight into a host or be passed through to a VM.
  - The adapter's own listing warns that SMART comes through incomplete or not at all, so the bay would refuse.
  - The HBA gives full SCSI health data, `sg_format` for 520-byte drives, and SANITIZE.  That's the Purge route.
  - Either way, `bayd` needs a SCSI SMART reader and grader: grown defect list, error counter log.
- **Phase 2 on hardware** (needs Val first):
  - Does `systemctl set-property --runtime` drop a removed partition from the device filter, and how long is a new partition blocked after insert?
  - Does the link's key reach it through `LoadCredential=`?  The fallback is `root:zawiper 0640`.
  - Does the read (smartctl, blkid, mounts in the private namespace) still work under the hardened `bayd@`?
  - Does `Sys.Eject` spin the drive down through the ASMedia bridge?
- **What a buyer can reach:** decided 2026-10-25 — nothing, unless they ask.  Records stay on the hub.  A listing says the cryptographic proof is available on request, and the record page's `proof.zip` is what gets sent: `record.json`, its signature, `record.md`, the SMART files, and `za-wiper.pub`.  Every check in `record.md` points at the files beside it.  Most buyers won't care, and that's fine.
  - Still open: where a buyer who does care gets `zverify`.  Until there's an answer, `record.md` carries a check that needs only `openssl` and `cmp`, plus `ssh-keygen` for the signature.
  - At an institution, public proof may matter.  `-site-url` puts the record's address and a QR code back on the sheet once a hub has a public address to give.  Behind Keycloak, "public" can mean anyone with an institutional login: a department checks its own drives' records without anyone mailing a zip.
- **Zim:** the seed pipeline stages whatever the catalog has newest, so a hold in a manifest is only a comment to it.  Holds need to be real (a pinned `file:` in the manifest) before the next push that should run seed.

---

3-2-1-1-0.  The last 0 means a drive proven blank, and Za Wiper is how we prove it.
