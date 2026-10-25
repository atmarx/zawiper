# Za Wiper

The drive bays from [docs/za-wiper.md](../docs/za-wiper.md).  Phase 2: many bays on many dock hosts, one hub, and each bay's scope held by the kernel rather than by bayd's good intentions.

- **`bayd@<bay>`** is a bay's disk side, root, one per bay.  udev starts it when the bay's selector matches a disk, and it stops when the drive leaves.  It runs with `DevicePolicy=closed`: of all the disks on the host, it can open only `/dev/zawiper/<bay>` and that drive's partitions and sg node.  It has no network.  It answers on `/run/zawiper/<bay>/bayd.sock`, keeps its job in `/var/lib/zawiper/<bay>/job.json`, and leaves finished records in `/var/lib/zawiper/<bay>/spool/`.
- **`bayd-link@<bay>`** is its network side, running as `zawiper` with no devices at all.  It dials out to the hub over mutual TLS with the bay's own client certificate, carries commands down to the socket, and uploads the spool.  A dock host listens on nothing.
- **`zaweb`** is the hub, a container on hub-host: the wall of bays, the records, the certificates.  Browsers reach it through Caddy at `zawiper.example.org` (8460).  Bays reach it directly on 8461.

The disk side runs as root in group `zawiper`, so the socket, the state file and the spool belong to the link's group without a chown anywhere.

## Work on it

```
make test
make build          # bin/bayd, bin/bayd-link, bin/zaweb, bin/bayrules
truncate -s 1000204886016 /tmp/bay.img          # a "drive"; delete it to pull it
bin/bayd -dev-bay /tmp/bay.img -dev-smart internal/smart/testdata/st31000528as.json \
         -socket $XDG_RUNTIME_DIR/zw.sock -records /tmp/zw
```

## The hub, on hub-host

Once, by hand:

1. **The CA.**  On a machine that isn't a server, with the [step CLI](https://smallstep.com/docs/step-cli):
   ```
   export CA_DIR=/path/to/dr-keys/zawiper     # no default, on purpose
   deploy/ca.sh init                           # the root, encrypted; it stays with the DR keys
   deploy/ca.sh hub zawiper.example.org       # SANs: that, hub-host, 192.0.2.10
   ```
   Certificates land in `./zawiper-certs/` (or `OUT_DIR`).  The script refuses to write the root or any key inside a git work tree.
2. **The stack's directories:**
   ```
   sudo install -d -o 8460 -g 8460 /opt/zawiper/data
   sudo install -d /opt/zawiper/tls
   # copy zawiper-certs/hub/{ca.crt,hub.crt,hub.key} into /opt/zawiper/tls/
   sudo chown 8460 /opt/zawiper/tls/hub.key && sudo chmod 0400 /opt/zawiper/tls/hub.key
   ```
   `/opt/zawiper/data` is the records, and it's in the hourly restic job with the rest of `/opt`.
3. **Caddy** (Val, homelab `gateway-stack/Caddyfile`): point `zawiper.example.org` at `zaweb:8460`, keeping the private-ranges guard and the WAF.  zaweb joins `gateway-stack_gateway`, and 8460 isn't published on the host, so Caddy is the only way to the page.

After that, CI does it: `.woodpecker/zawiper.yml` runs vet and test on every push touching `zawiper/`, and on master builds `zawiper/zaweb` on hub-host's own Docker and runs `deploy/hub/compose.yml` from `/opt/zawiper/`.  By hand, from `zawiper/`: `make image`, copy the compose file to `/opt/zawiper/`, and `docker compose up -d` there.

## A dock host

Bays never ship through CI (spec §8).  They go out by the fleet play, or by hand with `make bay HOST=<host>`, which builds the bundle (`bin/zawiper-bay-*.tar.gz`: bayd, bayd-link, bayrules, the units, `install-bay.sh`), copies it over, and runs the installer under your own sudo.

The host needs `smartmontools` and `util-linux` (blkid, blockdev, unshare, mount), and systemd with udev.

### Provision a bay

1. **Find the selector.**  For a USB dock: unpack the bundle on the host, put a drive in, and run `sudo bin/bayd ports`.  It lists each USB disk and the port it's on (`10-1`), as this machine's sysfs names it.  For a SAS HBA port: `ls -l /dev/disk/by-path/ | grep sas` and take the whole disk's name (`pci-0000:03:00.0-sas-phy2-lun-0`, not a `-part`).  An HBA qualifies only in IT/HBA mode (`mpt3sas`), never behind `megaraid_sas` (spec §4.1).
2. **Write `/etc/zawiper/bays/<name>.toml`**, root-owned.  The name is 1-32 of `a-z 0-9 _ -`, and it must match the file's name:
   ```
   name = "dock-a"
   usb_port = "10-1"                              # or: sas_path = "pci-0000:03:00.0-sas-phy2-lun-0"
   smart_type = "sat"                             # sat for USB-SATA; scsi for SAS (the default with sas_path)
   caps = "wipe"                                  # wipe, load, sas; none means it only reads
   ```
   Exactly one selector.  Two bays with the same selector, an unknown key, or a leftover `port =` all fail the install, before it changes anything.
3. **Its certificate.**  Where the CA lives: `deploy/ca.sh bay <host> <name>` makes a one-year client certificate, CN `bay/<host>/<name>`.  Copy `zawiper-certs/<host>/{ca.crt,<name>.crt,<name>.key}` to the host's `/etc/zawiper/tls/`.  The key is `root:root 0600`; the link gets it as a systemd credential, so each link reads only its own.
4. **Install:** `make bay HOST=<host>`, or `sudo deploy/install-bay.sh [--hub host:8461]` from an unpacked bundle.  The hub address goes in `/etc/zawiper/link.env` the first time; the default is `zawiper.example.org:8461`.

### What the installer does

- Checks every bay config with the same parser bayd uses, and generates `/etc/udev/rules.d/61-zawiper.rules` and a drop-in per bay binding `bayd@<bay>` to `/dev/zawiper/<bay>`.
- Installs `/usr/local/bin/{bayd,bayd-link}`, `bayd@.service`, `bayd-link@.service`, the `zawiper` user (sysusers.d), and loads `sg`.
- Enables and restarts `bayd-link@<bay>` for each bay that has its certificate, and disables the link of any bay whose config is gone.
- Re-triggers udev, so a drive already in a bay gets its `/dev/zawiper/` names and its `bayd@`.
- Never writes a bay config or a key, and **never restarts a running `bayd@`**: it might be mid-wipe.  It says so, and you restart it when the bay is idle.

### Checking the scope

```
ls -l /dev/zawiper/                                   # dock-a -> ../sdc, dock-a-part1 -> ../sdc1, dock-a-sg -> ../sg2
systemctl show bayd@dock-a -p DevicePolicy -p DeviceAllow
systemctl status bayd@dock-a bayd-link@dock-a
```

Partitions come and go while the unit runs (the read finds them; a wipe destroys them), so the udev rule for each one runs `systemctl set-property --runtime bayd@<bay> 'DeviceAllow=/dev/zawiper/<bay>-partN rw'`.  It names the partition by its `/dev/zawiper/` link, never by `/dev/sdc1`, and systemd resolves those links each time it rebuilds the filter.  A partition that has gone drops out, and nothing outside the bay can ever be named.  If bayd can't open a partition (the filter hasn't caught up), it waits ten seconds and then reports the partition as unreadable rather than blank.

### Stopping a run

Nothing on the page stops a run (spec §4.3).  A person with root on the dock host can: `systemctl stop bayd@<bay>`.  The job comes back INTERRUPTED, like a power cut.

## Migrating dock-host from Phase 1

Phase 1 is one `bayd.service`, `/etc/zawiper/bay.toml`, a `zaweb` container on the same VM, and records in `/srv/zawiper`.

1. **Bring the hub up on hub-host** (above) and let CI deploy it.
2. **Move the records.**  From a machine that can reach both:
   `ssh dock-host 'sudo tar -C /srv/zawiper -cf - .' | ssh hub-host 'sudo tar -C /opt/zawiper/data -xf - && sudo chown -R 8460:8460 /opt/zawiper/data'`
3. **Name the bay and write its config.**  Run the installer once, and it stops before changing anything and prints the `/etc/zawiper/bays/<name>.toml` to write, with the old `port` as `usb_port`.
4. **Mint its certificate** (`deploy/ca.sh bay dock-host <name>`) into `/etc/zawiper/tls/`.
5. **Run `make bay HOST=192.0.2.18` again.**  The installer refuses while a `zwipe` unit is running.  Otherwise it disables and deletes `bayd.service`, removes the old `zaweb` container (only if its image is `zawiper/zaweb:local`) and `/usr/local/bin/zwipe`, moves `bay.toml` aside to `bay.toml.phase1`, and installs Phase 2.
6. **Check the wall** at `zawiper.example.org`, and once the records show up there, remove `/srv/zawiper` on dock-host.  The installer reminds you while it's still there.
