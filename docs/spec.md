# Za Wiper — drive bays that read, wipe, prove, and sometimes fill with an Ark

Spec v0.5, 2026-10-29.  Idea and name: xram.  Written with Zim, an AI collaborator (Claude); the calls are xram's, marked with a name and a date.

- **v0.1** (2026-10-09): one bay, read and wipe, for The Ark.
- **v0.2** (2026-10-23): the decisions in §2.
- **v0.3** (2026-10-24): many bays on many hosts behind one hub, the record as a certificate, one shared view with no abort, and each bay's scope enforced by the kernel.  The first real wipe ran that day.
- **v0.4** (2026-10-27): its own repo, and nothing in it tied to one deployment.  Bring-up is `docs/bringup.md`; day-to-day is `docs/operating.md`.
- **v0.5** (2026-10-29): health under load, with tags on every note (§6.4, §6.6); Purge for flash, through sanitize where the bridge passes it (§6.7); and two more things the docks taught us (§3).

## 1. What it is

Drive bays on any Linux host assigned to the job, each run by a `bayd`, all reporting to one hub.  The hub's page shows every bay on one wall, and every record lands there, filed by the drive's serial.

It runs fine as one PC with one bay.  The point is when it isn't: a bay in every lab that retires drives, and one place an auditor can ask what happened to a serial, which machine wiped it, how, and when (xram, 2026-10-27).

Two uses, one codebase:
- **Resale.**  A buyer wants three things: a blank drive, proof nothing's left on it, and that drive's own health.  The record is that proof, and it prints as a certificate (§7).
- **Disposal at an institution.**  Every drive that leaves needs a recorded, auditable sanitization, done wherever the drive happens to be, and reported to one place.

Loading an Ark is the rarer job, for drives given as gifts or handed out.

1. **Put a drive in.**  Its read-only facts appear on their own: identity, SMART, the filesystems on it, and how much file data they hold.  The button on the page is **Re-read**.
2. **Look, then decide.**  If it's okay to wipe, press **Wipe** or **Wipe + Load**.  The confirmation restates what will be destroyed.
3. **Walk away.**  The job overwrites every sector, reads every sector back, compares SMART before and after, and, for Wipe + Load, lays down an Ark and fills the rest.
4. **Look it up later.**  Every drive lands in a searchable list of serial numbers.  Click one to see its record.  Print it, or save it as a PDF to email with the drive.

The page is for a private network.  A single-operator hub has no login; an institution puts sign-in in front of it (§2, *Who it's for*).  By default a record leaves on paper, as a PDF, or as a `proof.zip`, never as a public link.

### Goals, in order

1. **Can't be pointed at the wrong disk.**  This outranks everything else.
2. **Prove the wipe, to a stranger.**  Write every sector, read every sector back, and say exactly what happened, in a certificate a buyer can check without trusting us (§7).
3. **Keep a record per serial** that you can still read when the software is gone.
4. **Load an Ark** and fill the drive to the last byte.
5. **Survive real life:** the browser closes, the host reboots, someone yanks the drive.

### Non-goals

- Access from the internet, or any kind of public lookup, unless a deployment chooses one (`-site-url`, §11).
- A file browser on the page.  The disk side has no network and the hub has no device, so drive contents never cross the link.  Records never hold what was on a drive either.  The inspect page shows the partitions, labels and used space, and a wipe needs the serial typed in, so you get a last look before anything is erased (xram, 2026-10-25).  For more than a look, either eject the drive and mount it read-only on a workstation, or run `bayd browse <bay>` at a root shell on the dock host: it mounts each filesystem read-only, `noexec,nosuid,nodev`, with no journal replay, for SFTP from another machine.  That path runs over SSH to the dock host, never through the hub (`8792576`; `docs/operating.md`).
- Being someone else's audit.  But the target is to **satisfy NIST SP 800-88 Rev. 2** (xram, 2026-10-24).  Rev. 2 replaced Rev. 1 in September 2025.  Apart from crypto erase, it no longer lists techniques itself; it defers to IEEE 2883-2022.  So, for each media type:
  - **Purge where the drive allows it.**  That means the drive's own sanitize command (overwrite, block erase, or crypto erase), from IEEE 2883's table for that interface.
  - **Clear otherwise.**  That's our keystream overwrite with full read-back.  It can't reach sectors the drive has remapped.
  - **Every sanitization is verified,** and every certificate names the level, the technique, and the standard clause.
  - **Flash only ever gets firmware sanitize.**  An overwrite proves nothing on an SSD.
  - We never claim Purge when the command wasn't available or didn't get through the bridge.
  - An older drive (ATA8-ACS, which predates SANITIZE) may offer only ATA Security Erase.  IEEE 2883 counts it as Purge in Enhanced Erase mode (§8.4.3.2.1) and as Clear in Normal mode (§8.4.2.3), and recommends neither without the manufacturer confirming the model's implementation.  The ASM1156 bridge passes it (2026-10-25, §11).

## 2. Decisions (xram, 2026-10-23 and 24)

| Question | Call |
|---|---|
| Who sees it | The private network the hub serves.  The record ships as print, PDF or `proof.zip`, matched to the serial. |
| Confirmation | No typed serial, no hold-to-arm, no countdown.  Show the filesystems found and how much file data they hold, then one deliberate confirm. |
| On insert | Read-only facts load automatically.  The button is Re-read. |
| Jobs | **Wipe**, and **Wipe + Load** (an Ark loadout). |
| After | Searchable list of serials → click → results. |
| Storage | Files per drive (§7).  The list is built by scanning them, so no database is needed at this scale. |
| While it runs | **One shared view.**  Everyone who opens the page sees the same thing, and nothing on it belongs to anyone's session. |
| Stopping a run | **No abort on the page.**  Once a run starts, nobody watching can break it.  We all just get to watch. |
| The look | The wipe screen is MS-DOS DEFRAG: a grid of blocks being wiped, then verified, then loaded (§6.5). |
| What it's for | Wiping drives with proof: for resale, where the buyer wants a blank drive, a certificate and the drive's health; and for an institution's disposal, where the record is what an auditor asks for.  Wipe + Load is the gift case. |
| Many bays | One web interface, many bays, like OSDs in Ceph.  Each bay checks in and can take disks.  One hub (`zaweb` plus the records) talks to all of them.  A drive's history follows its identity, not the dock it was in (§8). |
| Scope | A USB-SATA dock, a USB-SAS adapter, and a port on a SAS card all work the same way.  Each bay's service is scoped to one device, by USB port or hardware ID.  What's in its config is what it can touch, and nothing else. |
| Who it's for | Two shapes of deployment, one codebase.  **One operator:** no login, drives for sale.  **An institution:** OIDC sign-in, custody fields (asset tag, ticket, owner, disposition), hash-chained records, and destruction records for drives that can't be wiped.  **Who can run a bay is set per host; who can watch is everyone** (xram, 2026-10-27; per host since 2026-10-30): each dock host belongs to one group, allowed to wipe, ack and eject at every bay on it, matched against the sign-in's group claim (a Keycloak group, a Globus group, an AD group, whatever the identity provider carries).  The hub keeps the host-to-group list, not the bay's own config, so a dock host can't move itself into another department.  Anyone signed in can watch the whole wall, search any serial across the organization, and see its history: every job, and the host and bay that ran it.  **Departments on the board, never people** (xram, 2026-10-30): the board and the anonymous pulse show departments (a host's group), never the person who ran a job.  Who ran it stays in the record, for audit.  A department with fewer than three operators is folded into "other", so the board can't name a person by elimination.  The board is trends over time to look at and share, not a contest to win: someone gets bragging rights, nobody chases points.  Its first questions (xram, 2026-10-30): which departments wiped the most drives, and the most TB, over the past day, month and year.  Proposed beside them: days at risk (retired to certified) and dying drives caught.  Records from before a host had a group count under the host.  The code stays brand-free; each deployment's identity, groups and certificate letterhead live in its own config. |
| Load payload | The hub serves staging, and `bayd` pulls it over plain HTTP with range requests.  No tusd, and no network mount on dock hosts.  Sending a loadout over the network twice is fine. |

## 3. Hardware: what the first docks taught us

The first dock was a two-slot USB 3 caddy on an ASMedia ASM1156-PM bridge: first passed through to a VM, then on bare metal.  The second was a single-slot USB-C adapter for 2.5" drives.  What they taught, in the order they taught it:

- **USB passthrough can't take a swap (2026-10-25).**  The dock re-enumerates on every insert and pull.  The first time it did, QEMU's libusb host passthrough hit `usb_host_ep_update: Assertion 'alt < …num_altsetting' failed` and the VM died with it.  On the reboot the guest named the port `5-1`, not `10-1`.  A dock behind a VM needs a whole USB controller passed through (PCIe), so the guest kernel sees the hotplug; or the dock goes on bare metal.
- **One USB device, two slots** (ASMedia ASM1156-PM bridge).  A swap in either slot resets both.  Nothing the host sees names a physical slot.  **One drive per bay:** `bayd` refuses to act if it sees more than one LUN.  `usb_lun` makes each slot its own bay (§8).
- **Both slots have reported a placeholder size**: 4.14 GB, for a drive that didn't identify itself properly as it came up.  Refuse a drive whose block size doesn't match what SMART reports, or whose SMART can't be read.
- **The two slots are one failure domain** (2026-10-27).  To the kernel the dock is one SCSI host with two LUNs, and its error handling stops the whole host while it recovers from one LUN's timeout.  A drive that doesn't answer in one slot stalls the other: a ST4000NM0033 that came up as 4.14 GB held slot 0's wipe to 70 MB/s and falling, and it ran at 165 MB/s once that drive was out.  A dying ST3000DM001 hung long enough to reset the bridge, and took the healthy drive in the other slot down with it.  So a drive that's refused for not answering comes out before the other slot starts a wipe, and the refusal says so.  A suspect drive goes in alone.
- **`smartctl -d sat` works through the ASM1156,** in a VM and on bare metal.
- **Key everything on the SMART serial.**  The kernel's serial was all zeros with two drives in the enclosure.
- **ATA Security Erase gets through this bridge** (§11).
- **Sustained throughput is the drive's, not the bridge's.**  A healthy 3 TB 7,200 rpm drive wrote at 160–170 MB/s on the outer edge through one ASM1156 slot, well under the 5 Gb/s link's ~450 MB/s.  A 2.5" 5,400 rpm drive on a UAS USB-C adapter ran about 105.
- **The device filter can go stale (2026-10-28).**  A ST2000DM001 came up in stages in dock-host-2's slot 0: a zero-size disk, a UAS reset that re-created the SCSI device, then the real 2 TB disk 25 seconds later.  `bayd@bay1` started on the last of them with `/dev/zawiper/bay1` in place, and the kernel still refused it every open for the next 28 hours.  Its refusal blamed the bridge, while `smartctl` as root and `smartd` read the drive fine; a restart fixed it.  The cause is unproven.  `bayd` now checks that it can open its own drive before reading it.  If it can't, it exits once per insertion and `Restart=always` brings it back with the filter built afresh; if that doesn't help, the refusal names the filter.  It never restarts under a job.
- **A bridge that passes IDENTIFY can still block SANITIZE (2026-10-29).**  A Crucial MX500 on the Insignia NS-PACS2B25 (a bare USB-C-to-SATA cable for 2.5" drives, no case) listed the SANITIZE feature set, BLOCK ERASE and CRYPTO SCRAMBLE, but `hdparm --sanitize-status` came back `Input/output error`, reason not reported.  The same bridge passes SMART, so it's refusing the command, not the replies.  Sanitize support belongs to the drive and the bridge together, and a bay asks before it offers Purge (§6.7).
- **Old RAID members come in every flavour.**  The same MX500 was an md member, and mdadm on a dock host still running the v0.3 rules assembled a stray, inactive `md127` around it; the bay refused it as in use, as it should.  The rules from 2026-10-27 stop that.  The ST2000DM001 carried Dell PERC metadata (`ddf_raid_member`), which is just a label to overwrite.
- **A DCO can disagree with the drive by one (2026-10-29).**  An EDGE SE847 SSD (256 GB) on the Insignia cable: `hdparm -N` said 500,118,192 of 500,118,192, HPA disabled; `--dco-identify` said 500,118,193.  The bay refused it for one hidden sector and told xram to run `hdparm -N p500118193`, which can't work: -N only lifts an HPA, never past the native size, and the drive said `SET_MAX_ADDRESS_EXT failed`.  The refusal now tells an HPA from a DCO and gives each its own fix.  Still open: a gap of exactly one is what firmware produces when it fills in DCO IDENTIFY with a sector count where the standard asks for the last address, and a DCO restore that leaves it in place would prove that's all it is.
- **Still unmeasured:** a udev rule that authorizes storage interfaces only on the bay's port.

## 4. Safety

The visible part is one confirm.  The real guards are the ones that never show up on the page.

### 4.1 Invisible, always on

- **The API can't name a disk.**  No endpoint takes a device path or a major:minor number.  Each bay's selector lives in `/etc/zawiper/bays/<bay>.toml`, written once by a person provisioning the host.  The selector is a USB port path, a SAS by-path (`pci-…-sas-phyN-lun-0`), or an enclosure slot.  The hub can address a bay, never a device.
- **The kernel enforces the scope, not `bayd`'s code.**  A udev rule matches the selector and creates `/dev/zawiper/<bay>`, plus `/dev/zawiper/<bay>-sg` for SAS tools.  The bay's disk unit runs with `DevicePolicy=closed` and `DeviceAllow=` those nodes only, so even as root the cgroup device filter refuses any other disk (§8).
- **RAID cards only in HBA/IT mode.**  Behind `megaraid`, SMART and passthrough go through one controller-wide ioctl node that can reach every disk on the card.  That breaks the scope, so the bay refuses it.
- **A start carries what the confirm showed.**  Wipe and Load name the bay, the drive identity, and the insertion session the confirm was drawn from.  `bayd` refuses if that isn't what it holds right now.  So a stale confirm, a swapped drive, or a hub that's confused all fail closed.
- **One LUN, sane size.**  `bayd` refuses if the port shows two block devices, if the size doesn't match SMART, or if the size is outside 8 GiB–64 TiB.
- **Never a host disk.**  `bayd` refuses anything mounted by the host, or anything holding a root, Docker, or LVM/ZFS member that the host is using.  A VM's own disks are `sd*`/`vd*` too, which is exactly what `ark stamp`'s guard gets wrong (§9).
- **Identity is pinned for the whole job.**  Before each destructive phase, and at least every 100 GiB, `bayd` re-reads serial, size, and WWN.  Any change aborts the job and raises **ALARM**.
- **Fail closed.**  On a write error, read mismatch, or vanished device, the job ends **FAILED** with the byte offset, and nothing continues.  A job that was running when the host went down comes back **INTERRUPTED**.  Nothing resumes by itself, and nothing is ever reported done unless it was proven done.

### 4.2 Visible: the confirm

> **Erase Seagate ST31000528AS, serial 9VPAX9NA, 1.00 TB?**
> Found on it: GPT · 2 partitions · ext4 `data` (312 GB of files) · NTFS `Windows` (88 GB of files).
> [ Cancel ]   [ Erase this drive ]

- **"Found on it" comes from a read-only peek.**  It lists the signature scan, then mounts each recognized filesystem `ro,noexec,nosuid,nodev` (plus `noload` for ext4) in a throwaway mount namespace, measures used space, and unmounts.
- **"Nothing found" is said plainly:** "No partition table, no filesystems.  Looks blank already."
- **Backup drives get flagged.**  A label starting with `backup-`, a restic repository, or a PBS datastore turns the card amber, and the confirm button reads **Erase this backup drive**.  md, LVM and ZFS members get the same flag, since they could be holding anything.  The bays' udev rules blank `ID_FS_TYPE` on bay devices, so a dock host with mdadm or LVM installed never assembles a bay drive's array: it stays a drive to wipe (xram, 2026-10-27).  Backup rotation drives often go through the same docks, so this costs nothing and catches the 2 a.m. mistake.
- **The confirm resets if the drive's facts change** while it's on screen.

### 4.3 The page itself

- **Network:** the page sits behind a reverse proxy with TLS that admits only the private ranges the hub serves.  zaweb also answers only the hostnames it's given (`-hosts`), which closes DNS rebinding.
- **Dock hosts don't listen.**  Each bay dials out to the hub over TLS, with its own client certificate (CN `bay/<host>/<name>`, like an OSD's keyring), and keeps that one connection open.  Commands come down it, and status and records go up.  A dock host exposes no port.  The hub is the trust point: owning it means being able to wipe whatever is sitting in a bay.  So it stays small, on a backed-up host.
- **Browser:** each mutating call needs a matching `Host` and `Origin`, `Content-Type: application/json` (which forces a CORS preflight the server refuses), and an `X-Za-Wiper: 1` header.  A malicious page open in a LAN browser can't start a wipe.
- **One job at a time.**
- **One shared view.**  The page draws only what `bayd` reports.  There's no per-visitor state, no "your job", and no control that exists for one viewer and not another.  Ten people watching see the same frame.  The confirm dialog belongs to whoever is about to start a run.  Once they press it, the run belongs to the bay.
- **Nothing on the page stops a run.**  `bayd` stops a job only on its own evidence (§4.1: identity changed, write error, mismatch).  The only other stop is a person with root on the dock host, at a shell: `systemctl stop bayd@<bay>`.  The job then comes back **INTERRUPTED**, like a power cut.

## 5. States

```
EMPTY ─insert─► READING ─► PRESENT ─confirm─► RUNNING ─► DONE
  ▲                ▲          │                  │ ├──► FAILED
  │                └─Re-read──┘                  │ └──► ALARM (identity changed)
  │                                              └ host/bayd died ─► INTERRUPTED
  └──────────── remove / Eject (from any settled state) ◄───────────
```

| State | The page says |
|---|---|
| `EMPTY` | "Nothing in the bay." Search box, last few drives. |
| `READING` | The step it's on and when that started ("Asking SMART (smartctl), since 09:34:35."), so a hung drive shows up as a step that doesn't end.  A drive that doesn't answer SMART, or disagrees with the bridge about its size, gets no further reads: it can't be filed or wiped, and every command it times out stalls the bridge. |
| `PRESENT` | Drive card, Re-read, Wipe, Wipe + Load. |
| `RUNNING` | **Do not pull the drive.**  "Once started, this process cannot be stopped from here."  The surface map (§6.5), phase, MB/s, ETA, temperature, errors, bayd's CPU and the host's.  A speed scale (slow, 5400, 7200, 10k, 15k, SSD, NVMe, fast) marks the current rate, shades what the slower of the USB and SATA links can't reach, and guesses the drive's kind from its peak, beside what SMART claims (xram, 2026-10-27).  No buttons. |
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
| 3 | **Overwrite** | Every sector, with a seeded keystream (ChaCha20 or AES-CTR, random key per job, aligned blocks ≥ 4 MiB, `O_DIRECT`).  Throughput sampled every few seconds. |
| 4 | **Verify** | Regenerate the keystream, read every sector back cold, and compare.  This is also a capacity test: a drive that lies about its size fails at the point where it wraps. |
| 5 | SMART after | Diff of reallocated, pending, uncorrectable, CRC and temperature. |
| 6 | Record | §7.  The keystream is left on the drive as it is.  It carries no partition table or filesystem signature, so the drive reads as blank.  Leaving it untouched is what lets a buyer re-check it. |

### 6.3 Wipe + Load

Phases 1–5 of Wipe, then the load.  The payload comes from the hub, not from a network mount on the dock host:
- **The hub serves staging read-only over HTTP,** following The Ark's staging symlink farm.  It holds the staging mount; dock hosts never see it.
- **First comes the loadout's list:** files, sizes, and each ZIM's SHA-256 from Kiwix's `.meta4`.  `bayd` checks that it fits before writing anything.
- **`bayd` pulls each file onto the target drive.**  A dropped connection resumes with a range request from the partial file's length.  The partial file is the bookmark, so nothing is staged twice.
- **The load is checked against Kiwix's hashes,** not against the transfer.  So one check catches a bad link, a bad cable, and a bad sector alike.
- **On cost:** every byte crosses the network twice, staging → hub → dock.  That's accepted: one bay over gigabit runs about as fast as a drive writes, and loads are rare.

| # | Phase | Notes |
|---|---|---|
| 6 | Partition and format | exFAT, label `THE-ARK`. |
| 7 | **Load** | Pull the chosen loadout from the hub, plus `--pack=` if one is picked.  The page only offers loadouts that fit the drive. |
| 8 | **Verify the load** | Unmount, remount, re-read every file, and compare its SHA-256 with Kiwix's. |
| 9 | Records on the drive | `ZA-WIPER.txt` (the human copy, with the viper on top) and `ZA-WIPER.json`. |
| 10 | **Ballast** | Keystream into `DELETE_THIS_IF_YOU_NEED_SPACE` until `ENOSPC`, then check that a 1-byte write fails too.  The final record goes into a 16 MiB reserve freed at the end. |
| 11 | Sync, unmount, eject | |

The payload source is the staging symlink farm, so the copy follows links (`--copy-links` semantics).  It copies **what the manifest names**, including held builds like the Wikisource ones, which is the opposite of today's `ark stamp` filter (§9).

### 6.4 Grade

| Grade | When |
|---|---|
| **PASS** | No mismatches, no read errors, no SMART counts rising, no stalls. |
| **PASS WITH NOTES** | Counts that are non-zero but stable; a few stalls; high temperature; a SMART attribute that tripped in the past.  The test drive is this case: airflow temperature `In_the_past`. |
| **FAIL — DO NOT USE** | Any mismatch or read error, SMART counts rising during the job, or a size or identity inconsistency.  A FAIL drive can't be loaded. |

Thresholds live in a config file, with the reason next to each one.

**Every note carries a tag** (xram, 2026-10-29).  The full sentence stays on the record page; the tag is the pill beside the grade, on the wall card, the drive's history and the certificate.  A note is a tag, a severity, who it's about, the measured value, and the sentence.

- **Values, not scores.**  `REALLOC 8` and `HOT 52 °C` are facts anyone can check against the SMART files.  A combined health score would be a weighting we made up, signed as if it were a measurement.
- **An arrow means it rose during the job** (`PENDING 0→6`).  That's the strongest sign of a drive about to go.
- **Who it's about matters to a buyer.**  CRC errors are nearly always the cable or the bridge, and today they read like a mark against the drive.

| Pill | Severity | Drawn as | Tags |
|---|---|---|---|
| red | fail | filled | `PENDING 0→6` (any watched count but CRC rising), `BAD 4.10 KB` (bytes that didn't read back), `FAILING` (the drive's own verdict), `THRESH <attr>` (at its threshold now), `IDENTITY` (a different drive answered mid-job); to come, `SELFTEST` (our own run failed) |
| amber | a note against the drive | solid | `REALLOC 8`, `PENDING 2`, `OFFLINE-UNC 1`, `UNCORR 1`, `HOT 52 °C`, `SLOW 3`, `ERRLOG 12` or `12→14`, `PAST <attr>`, `SELFTEST log`; to come, `LATENCY 2.1 s` |
| cyan | not the drive | dashed | `CRC 14` or `1→3` (cable or bridge), `BRIDGE` (SMART partly blocked), `NO-SMART`, `NO-STATUS`, `PARTIAL <how>`, `ENDED <state>`, `INTERRUPTED` |

Built 2026-10-29: every note `bayd` writes has its finding, and the grade comes out of them exactly as before.  `SLOW` counts blocks until §6.6 groups them into regions.

The shapes carry the severity on a black-and-white printout.  The wall card shows three pills and a count of the rest.  On the hub, tags are what the drives page filters on: every rising count, every `SLOW` from one bay, every `CRC` through one dock.

The tags go in a new field beside `notes`.  Records signed before it keep their sentences and show no pills; the hub doesn't guess tags from old wording.

### 6.5 The surface map

The wipe screen is DEFRAG.  The drive is a grid of 1 GiB blocks.  It's 932 cells for a terabyte, laid out in text-mode rows and drawn in the IBM VGA 8×16 font (Ultimate Oldschool PC Font Pack, CC BY-SA 4.0, self-hosted).  The colors are the blue field and gray frames, with a status box and a legend below.

| Wall | Printout | Means |
|---|---|---|
| `░` | `░` | not reached yet |
| `W` / `r` | `W` / `R` | being written / being read back now, blinking, as DEFRAG had them |
| cyan `■` | `▓` | written |
| white `■` | `█` | read back and verified |
| magenta `▒` | `▒` | slow: over twice the median time of its 16 neighbours.  Neighbours, because a spinning drive slows toward the inner tracks on its own. |
| `B` | `B` | bad: a write or read error, or bytes that came back wrong |
| shelf colors | | Load: each ZIM lands as a run, colored by shelf.  The cells come from where the filesystem actually put the file, not from a cursor. |

The wall draws discrete squares, so the grid reads as blocks rather than a fill; the printout has no color, so it keeps the shades.  Past 1,600 blocks the wall groups them (a 4 TB drive is 3 GiB a cell) and says so under the legend.

A 1 GiB block takes seven seconds or so, so the map alone moves in steps.  Between blocks, the engine reports after every 8 MiB buffer, and the link's once-a-second poll carries it: the written and verified byte counts, the percentage and the clock all move every second.  Under the boxes, a **Throughput** box graphs each block's MB/s across the drive, outer edge first: cyan as written, white as read back, so the read-back visibly catches up.  It's the drive's own speed curve, falling toward the inner tracks, in half-block characters.

**Two layouts** (xram, 2026-10-29), a toggle in the header that each browser remembers.  Narrow draws the map 80 columns wide, as DEFRAG did.  Wide fills the window, puts the boxes side by side, and on a screen 1,600 pixels or wider draws the map grouped by no more than 6,400 cells instead of 1,600, so a 4 TB drive shows one block a cell.  A cell can't be finer than the job's own 1 GiB block.

**The arcade** (xram, 2026-10-29).  F10, or a click on the map's title bar, swaps the map for a game of breakout played with its blocks: a slow ▒ takes two hits and mends to ■ on the first, a bad B never breaks, the blocks march down a row at a time, sooner each time, and the first miss ends it.  It runs in the viewer's browser and talks to nothing.  The live map keeps updating underneath, hidden, and comes back the moment the game ends.  A test holds `arcade.js` to that: no fetch, no API path, no event stream.  Breakout opens the bill; more games can follow it.

`bayd` keeps the cell states, and the page gets them as one short string a tick.  Every viewer gets the same string (§4.3).  The final map, with each block's write and read times, goes into the record.  `record.md` and the printout carry it as text, so the map ships with the drive.

The first map came from `zwipe` (`cmd/zwipe`), the hand-run wipe for `9VPAX9NA` on 2026-10-24.  It's a root CLI with the same read and refusals as `bayd`, and the serial on the command line as the confirmation.  Phase 2 moved its engine into `bayd`, and the CLI is gone.

### 6.6 Health under load

The wipe is already the stress test: every sector written and read back, for hours.  A drive near failure usually shows it there first, as retries that make blocks slow, before SMART counts a single reallocation.  v0.5 measures that, grades it, and draws it (xram, 2026-10-29).  Z1F5K2CR would have failed on the first four alone.

1. **Slow regions.**  The surface map already marks slow blocks.  One is noise; a run of them in one part of the disk is a weak patch.  A region of slow blocks is a note (`SLOW n regions`, with where they are); a region that's slower on the read-back than it was on the write is a fail.  Where a region starts and ends is a threshold in the config, set from what real drives do.
2. **The speed curve,** each block's MB/s across the disk, which the wall's Throughput box already draws.  A healthy spinning drive falls smoothly to about half by the inner tracks; dips and spikes are the trouble.  The record gets it as a graph.
3. **Temperature through the job, and a ceiling.**  `bayd` reads SMART temperature once a minute and keeps the curve.  It backs off if a SMART read stalls, since this dock has reset under SMART traffic.  Drives that keep an SCT temperature history give it up once, at the end.

   **A bay treats a drive as well as a case with airflow would** (xram, 2026-10-29).  A bay may wipe hundreds of drives, and none should come out older than the wipe has to make it.  So the job has a ceiling: the drive's own recommended maximum where SCT reports one, else 55 °C for a spinning drive and 65 °C for flash, and a bay's config can set it lower.  Over it, `bayd` pauses between buffers until the drive is 5 °C under, then carries on.  A wipe isn't a race; a paused one proves the same.  The record keeps the curve, the peak, and how long the job waited for the drive to cool.

   Heat during a job is mostly where the bay puts the drive, so a job that had to wait is a cyan `PAUSED 12 min` against the bay, not the drive.  `HOT` stays a note against the drive when it arrives hot, or when it runs hotter than others of its model in the same bay (the hub's comparison, below).

   The first measurement (2026-10-29): a Crucial MX500 on the Insignia cable, bare, in open air on a desk, reached 52 °C mid-write.  No enclosure needed: still air was enough.  It was 42 °C a few minutes after xram put a fan on it, at the same 213 MB/s.  The speed never moved with the heat; it had stepped down from 350 MB/s at 30 GiB, when the drive's SLC cache filled.  That step is the drive working as designed.  Flash records it as information (`CACHE 30 GiB`, cyan), never as a slow region.
4. **The drive's own short self-test** before the overwrite: about two minutes, the vendor's verdict, pass or fail on the record.  The long test isn't needed; the read-back covers the surface.
5. **A few minutes of fio,** before the overwrite and never after, since anything written after it breaks `zverify`'s check of the drive against its record.  4 KiB random reads at queue depth 32 for the latency tail: a retrying drive shows reads that take seconds.  On flash, a sustained write until its fast cache runs out.  The only step here that adds a package to dock hosts.
6. **SMART before and after,** as now.  Any watched count rising under one full pass fails the drive.

A wipe becomes: read → confirm → self-test → fio → overwrite → read-back (temperature sampled throughout) → SMART after → grade → record.  Five or six minutes on a job of hours.

**The Health page** of a record gets three graphs, drawn by the hub as SVG in the wall's palette: speed across the disk, temperature over time, and the read-latency spread.  They go into the printout and `proof.zip` with the rest.

**The hub compares drives with each other.**  It has every record, so it can say a drive read 38% under the median of the others of its model.  Only like with like: the same model behind the same kind of bridge, because a bridge sets its own ceiling.  Nothing is said until it has seen five of them.  This is the one thing a single bay can't tell you, and it's often how a near failure first shows: one drive lagging its siblings.

### 6.7 Flash: Purge through sanitize

An overwrite gives flash no level (§1), and an SSD can't be checked cell by cell: spare and retired blocks sit behind the controller and never appear in a read.  So the level has to come from the drive's own command, completed (xram, 2026-10-29).

1. **Ask first.**  Before offering Purge, `bayd` sends SANITIZE STATUS, which changes nothing.  An error means this bridge won't carry it, and the bay offers the overwrite only, with a record that says why.
2. **Crypto scramble, then block erase.**  The scramble throws away the key a self-encrypting drive keeps all its data under.  A crypto erase is only as good as the vendor's firmware, and drive encryption has been caught short before (Crucial MX100–MX300 and Samsung, Radboud University, 2018).  The block erase doesn't depend on it, and leaves a pattern a read can check.  Sanitize runs in the background, survives a bridge reset, and needs no password; that's why it, and not Security Erase, is the route.
3. **Poll SANITIZE STATUS until it reports done.**  Purge only if both commands report success.
4. **Read every block back** against the drive's post-erase pattern.  One byte off fails it.  A clean read-back is evidence, not the level: a drive can return zeros because its map was cleared while the old data still sits in the chips.
5. **Carve it with PhotoRec,** an independent tool anyone can run, in a private RAM-only directory inside `bayd`'s sandbox.  The record lists the count and kinds of files found (zero, if the erase worked), and they're deleted unread.  Nothing recovered ever reaches a disk.  Reads cost flash next to nothing, so this is automatic for SSDs: about ten minutes for 250 GB through a USB 3 bridge.  On a spinning drive it's hours, and optional.
6. **SMART before and after,** with the wear and spare-block counts that SSDs keep.

Where the bridge won't pass sanitize, an SSD gets steps 4–6 on top of the overwrite, and the record says no level for flash.  A motherboard SATA port has no bridge to block anything, which is one more reason for the `ata_path` selector (§10).

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

- **The list is built by scanning** `drives/*/drive.json` at startup and after each job.  A fleet will see thousands of drives, not millions.  If that ever gets slow, a SQLite index can be rebuilt from the files whenever it's needed, but it's never the source of truth.
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
- **For resale, the sheet travels with the drive (xram, 2026-10-25).**  When a wipe finishes, the hub sends the certificate to a color printer, unattended.  It's folded in quarters and taped to the outside of the static bag, so it's laid out for the fold:
  - **The outside quarter is the label:** serial in large type, model and size, the verdict, the grade, the date, and "Signed proof on request" with the record ID.  It reads through tape without unfolding.  A hub given a public `-site-url` prints a QR code to the record there instead.
  - **The inside is the certificate,** with the surface map in color.
  - **A failed drive prints too,** as a red "not for sale" sheet, so it can't drift back into the sell pile.
  - **The same render saves as a PNG** for the listing: one image per drive, ready to post or give away.
- **Lots, for resale.**  Used drives sell best as lots of two or three matched drives.  Three is a mirror and a spare.  A lot is a named group of serials: same model, close in power-on hours.  It gets its own page and one PNG with every drive's surface map side by side.  The lot is graded by its worst drive.
- **Signed, from Phase 2.**  `record.json` is signed with `ssh-keygen -Y sign`, and the public key and the verification steps are printed on the certificate.  An unsigned certificate is just a nice PDF.
- **A verifier for buyers:** a short script, or a page on the certificate, that reads a drive and checks it against the published key.
- **Backups:** the hub's records directory, with its signing key, is the one thing in the system that can't be recreated.  It goes in the backup scope the day the hub ships.

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
  - **`bayd@<bay>`** is the disk side.  udev starts it when the selector's device appears, and it's bound to that device (`BindsTo=`), so it stops when the drive leaves.  `DeviceAllow=` is resolved fresh on every insert, which sidesteps a re-inserted drive getting a new minor number.  When that goes wrong anyway, `bayd` restarts once to rebuild it (§3).  It has no network (`IPAddressDeny=any`, unix sockets only).  It owns the read, the keystream writer and reader, the mounts, and the job, and it talks to its link over a unix socket in `/run/zawiper/<bay>/`.
  - A running job needs nothing from the link or the hub.  Either can restart without touching it.  If the drive leaves mid-job, the link reports **INTERRUPTED** from the job's state file.
  - The wall shows a bay as empty while its link is up and its disk unit isn't running, and as offline when its link is down.
  - `/etc/zawiper/bays/<bay>.toml` holds the selector, the SMART type (`sat` or `scsi`), and what the bay can do: `wipe`, `load`, `sas`.  A two-slot USB dock is one bay by default: its port is the selector, and the one-LUN refusal stays.  `usb_lun = "0"` and `usb_lun = "1"` split it into a bay per slot.  A swap in either slot resets both, and systemd stops the other slot's `bayd@` with its device, so a wipe running there ends INTERRUPTED and never gets a record.  Swap only when both bays are idle.
  - Commands, from the hub: `status`, `reread`, `wipe`, `load`, `ack`, `eject`.  No command takes a device, there's no "run this" command, and there's no abort (§4.3).
  - The host can be bare metal, or a VM with a whole USB or SAS controller passed through (§3: a passed-through USB device doesn't survive a swap).  A VM has the edge where it works: a mistake can't reach a hypervisor's own disks.
  - **Releases don't go through CI.**  A key that can install `bayd` can wipe whatever is in a bay.  So `bayd` ships by hand (`make bay`) or by a config-management play, run with a person's own sudo: a pinned binary, plus each bay's config and certificate.  Nothing installs it automatically.
- **The hub, `zaweb`**: Go, unprivileged, with no devices, in a container on a backed-up host.  CI may deploy it (`.woodpecker/hub.yml` is one way).
  - It holds the `bayd` connections, the records, and, for Load, a read-only staging mount.
  - It serves the wall of bays (one shared view, §4.3), the list, the records, and the certificates.  Live progress reaches browsers as server-sent events.
  - A bug in it can't write to a disk directly.  It can only send `bayd` a command, and `bayd` checks every one (§4.1).
- **Trust:** an offline root CA, made with the `step` CLI (`deploy/ca.sh`), with no CA server.  The root key stays offline, encrypted.  The hub gets a server certificate; each bay a one-year client certificate, CN `bay/<host>/<name>`.  The hub admits any bay the root signed, so retiring a bay or a leaked key before its certificate expires needs a list of revoked fingerprints on the hub (Next, §10).
- **Payload:** pulled from the hub (§6.3).  Dock hosts need no network mount.
- **Tests without a drive:** `bayd --bay-file` on a loop device, with device-mapper `error` and `delay` targets for read failures and stalls, and a wrap-around map that fakes a lying-capacity drive.
- **Code lives in** its own repo since 2026-10-27; before that, `zawiper/` in The Ark, and the history came along.
- **ntfy** on DONE, FAILED, INTERRUPTED, and ALARM.  A 20-hour job ends while you're asleep.

## 9. What this means for `ark stamp`

Za Wiper replaces `stamp` for drives that go through the bay, and the bugs v0.1 passed along are ours now:

1. **Its device guard treats every `/dev/sd*` as USB.**  Inside a VM that's false.  The port-pinned bay replaces the guard.
2. **`dd … || true` swallows a failed wipe** and prints "wipe complete" anyway.
3. **The sudo helper passes `mount`/`chown` arguments through unfiltered.**
4. **Nothing re-reads the drive after the clone.**
5. **It filters ZIMs by the catalog's newest filename**, which leaves held builds off the drive.

`stamp` stays usable at a shell.  Items 2 and 5 get fixed there too, since they're one-line bugs with real consequences.

## 10. Phases

| Phase | Deliverable |
|---|---|
| **1 — Read-only bay** | `bayd` read path, `zaweb` drive card, Re-read, inspect records, serial list, record page with print.  Nothing can write. |
| **2 — Wipe, networked** | The hub/`bayd` split with dial-out TLS, one `bayd` per bay.  The kernel enforces each bay's scope.  Records move to the hub.  The wipe in `bayd`, the live surface map on one shared wall, the SMART diff and the grade.  Signed certificates, with a published key and a buyer's verifier.  **Built 2026-10-25** and run end to end on a file: hub, link and `bayd` over mTLS, wipe, record to the hub, signed on arrival, checked by `zverify` and `ssh-keygen -Y verify`.  **First hardware wipe 2026-10-26:** Z1N4BVB3 (Constellation ES, 1 TB) in slot 0 of the two-slot dock, unattended, under `bayd@bay1` and its device filter.  Clear per IEEE 2883 §8.4.2.2 with full verification, every byte read back, PASS WITH NOTES (one reallocated sector, before and after).  Its signature checks out with `ssh-keygen -Y verify` against `/za-wiper.pub`.  ATA Enhanced Security Erase through the bridge was measured by hand on 2026-10-25 (§11).  Still to prove: the negative case, that `bayd@bay1` can't open the drive in `bay2`. |
| **Next** | v0.5: the tags and pills (§6.4), then health under load (§6.6), then Purge for flash (§6.7), tried first on the MX500 in the ASM1156 dock.  Purge for spinning drives through sanitize or Enhanced Security Erase, where the bay passes it (§11).  Revoked bay fingerprints on the hub.  SCSI SMART grading for SAS.  An `ata_path` selector, so a motherboard or backplane SATA port is a bay (`pci-0000:00:17.0-ata-1`, matched like `sas_path`): a desktop with every port cabled out to a drive shelf is how many groups already wipe, and it skips every USB bridge quirk in §3.  It needs AHCI hot-plug enabled per port, and drive power that can take a shelf spinning up at once (xram, 2026-10-27). |
| **3 — Wipe + Load** | The hub serves staging; `bayd` pulls with range requests.  Format, load, verify against Kiwix's hashes, ballast, reserve, on-drive records. |
| **4 — Polish** | ntfy, metrics, a backup-drive profile, a destruction note on a record. |
| **Institutional** | OIDC sign-in, an operator group per host with an org-wide view (§2), custody fields, hash-chained records, destruction records. |

## 11. Open

- **NIST 800-88 Rev. 2 / IEEE 2883:** read from IEEE 2883-2022 itself (2026-10-25; kept out of the repo, it's copyrighted).  What it settles:
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
- **SAS:** a USB-to-SAS adapter versus an LSI HBA in IT mode with SFF-8482 breakouts.  A PCIe RAID card qualifies only if it runs as a plain HBA (IT firmware, or a mode that loads `mpt3sas`/`mpt2sas`, not `megaraid_sas`).  It can go straight into a host or be passed through to a VM.
  - The adapter's own listing warns that SMART comes through incomplete or not at all, so the bay would refuse.
  - The HBA gives full SCSI health data, `sg_format` for 520-byte drives, and SANITIZE.  That's the Purge route.
  - Either way, `bayd` needs a SCSI SMART reader and grader: grown defect list, error counter log.
- **Phase 2 on hardware, still to confirm:**
  - Does `systemctl set-property --runtime` drop a removed partition from the device filter, and how long is a new partition blocked after insert?
  - Does the link's key reach it through `LoadCredential=`?  The fallback is `root:zawiper 0640`.
  - Does the read (smartctl, blkid, mounts in the private namespace) still work under the hardened `bayd@`?
  - Does `Sys.Eject` spin the drive down through the ASMedia bridge?
- **What a buyer can reach:** decided 2026-10-25 for resale — nothing, unless they ask.  Records stay on the hub.  A listing says the cryptographic proof is available on request, and the record page's `proof.zip` is what gets sent: `record.json`, its signature, `record.md`, the SMART files, and `za-wiper.pub`.  Every check in `record.md` points at the files beside it.  Most buyers won't care, and that's fine.
  - Still open: where a buyer who does care gets `zverify`.  Until there's an answer, `record.md` carries a check that needs only `openssl` and `cmp`, plus `ssh-keygen` for the signature.
  - At an institution, public proof may matter.  `-site-url` puts the record's address and a QR code back on the sheet once a hub has a public address to give.  Behind the institution's sign-in, "public" can mean anyone with a login: a department checks its own drives' records without anyone mailing a zip.

---

3-2-1-1-0.  The last 0 means a drive proven blank, and Za Wiper is how we prove it.
