# Bringing up Za Wiper

From nothing to a signed record of a wiped drive, in order.  The first time through takes an afternoon, most of it waiting on the first wipe.  After that, each new bay is a config file, a certificate and one command.

What the pieces are and why they're split this way is in [spec.md](spec.md).  The day-to-day (adding bays, checking scope, holding a bay, looking through a drive) is in [operating.md](operating.md).

## 0. Plan it

You need four kinds of machine.  They can be fewer machines than that, but not fewer roles.

| Role | What it needs | Notes |
|---|---|---|
| **The hub** | Docker with Compose v2, inbound TCP 8461 from the dock hosts, a reverse proxy with TLS for the page | It holds every record and the signing key.  Put it on a host that's backed up. |
| **The CA** | The [step CLI](https://smallstep.com/docs/step-cli) | Offline is best: a workstation, not a server.  The root key never goes on the hub or a bay. |
| **A dock host**, one or many | Linux with systemd and udev, `smartmontools`, `hdparm`, `util-linux` | Bare metal, or a VM with a whole USB or SAS controller passed through.  It listens on nothing. |
| **A build machine** | Go 1.24, `make` | Usually your workstation.  Dock hosts don't need Go. |

Pick these names before you start:

- **The page's name,** e.g. `zawiper.example.org`: what browsers use.  zaweb answers no other.
- **The link address,** e.g. `zawiper.example.org:8461`: what bays dial.  It can be the same name.  It must be in the hub certificate.
- **Each bay's host and name,** e.g. `lab-a` and `bay1`.  The bay name is 1–32 of `a-z 0-9 _ -`.  The pair shows up on every record, so pick names an auditor will understand in two years.

Then write them down where `make` can find them.  On the build machine, in your clone:

```
make site
```

That copies `site.example/` to `site/`, which git ignores.  `site/site.env` takes the link address (`HUB`) and the hub host (`HUB_HOST`), `site/hub/` the hub's settings, and `site/bays/<host>/` one file per bay.  It holds no keys, ever.  Make it a private repo of its own if you want your fleet's history.  [site.example/README.md](../site.example/README.md) has the details.

### Hardware that works, and what to avoid

- **A USB-SATA bridge has to pass SMART.**  Before you build a bay around a dock, plug a drive in and run `sudo smartctl -d sat -i /dev/sdX`.  If it doesn't show the drive's real serial, the bay will refuse every drive: the serial is what a record is filed under.  `sudo hdparm -N /dev/sdX` should answer too, which is the hidden-sector check.  The ASMedia ASM1156 passes both.
- **A multi-slot dock is one failure domain.**  To the kernel it's one SCSI host.  A drive that stops answering in one slot stalls the other, and a bridge reset takes both down.  Give each slot its own bay (`usb_lun`), swap drives only when every slot on the dock is idle, and put a suspect drive in alone.
- **Don't pass a USB device through to a VM.**  The dock re-enumerates on every swap, and QEMU's USB passthrough doesn't survive it.  Pass through the whole controller (PCIe), or run on bare metal.
- **SAS cards only as a plain HBA** (IT firmware, `mpt3sas`).  Behind `megaraid_sas`, one controller-wide device reaches every disk on the card, which breaks a bay's scope, so Za Wiper refuses it.
- **Bus-powered USB-C adapters are for 2.5" drives only.**  A 3.5" drive needs 12 V.
- **Give every bay airflow, even a bare cable.**  A wipe is hours of the drive working flat out, with none of the airflow a case would give it.  An SSD lying bare on a desk reached 52 °C mid-wipe in still air; a small fan pointed at it took it to 42 °C.  The bay pauses a job that runs over the drive's temperature ceiling, so a hot dock costs you time, but a fan costs less.

## 1. The CA

On the CA machine, in a directory that isn't inside a git work tree (the script refuses one that is):

```
export CA_DIR=/path/to/offline/zawiper-ca     # no default, on purpose
deploy/ca.sh init                              # the root, encrypted; you choose its password
deploy/ca.sh hub zawiper.example.org hub-host 192.0.2.10
```

`hub` takes the name the bays dial first, then any other name or address they might use.  The certificates land in `./zawiper-certs/` (or `OUT_DIR`).  `step` asks for the root's password each time it signs.  Type it yourself, at a real terminal.

The root key is the one secret that can mint a bay, so keep it encrypted and offline.  A password manager entry with the key attached works.  When you need it, copy it to a RAM-backed directory such as `$XDG_RUNTIME_DIR`, point `CA_DIR` there, and delete it when you're done.  On flash, a deleted file isn't erased.

## 2. The hub

On the hub host:

```
git clone <this repo> zawiper && cd zawiper
make image                                     # zawiper/zaweb:latest, built here, never pulled

sudo install -d -o 8460 -g 8460 /opt/zawiper/data
sudo install -d /opt/zawiper/tls
sudo cp …/zawiper-certs/hub/{ca.crt,hub.crt,hub.key} /opt/zawiper/tls/
sudo chown 8460 /opt/zawiper/tls/hub.key && sudo chmod 0400 /opt/zawiper/tls/hub.key

sudo cp deploy/hub/compose.yml /opt/zawiper/
```

Fill in `site/hub/.env` on the build machine (the page's name and the timezone), then `make hub-site` to put it in `/opt/zawiper/`.  Back on the hub host:

```
cd /opt/zawiper && sudo docker compose up -d
```

The page is published on `127.0.0.1:8460` only.  Put your reverse proxy in front of it, with TLS, admitting only the networks that should see the wall.  With Caddy:

```
zawiper.example.org {
    @outside not remote_ip private_ranges
    respond @outside 403
    reverse_proxy 127.0.0.1:8460
}
```

If your proxy runs in a container on its own Docker network, don't publish 8460 at all: fill in `site/hub/compose.override.yml` with the network's name, and `make hub-site` puts it beside the compose file, where Compose merges it in.  Otherwise delete that file from `site/hub/`.

The links come in on **8461**, mutual TLS, straight to zaweb with no proxy in between: each bay shows the hub its own certificate.  Open 8461 to your dock hosts and nothing else.

Check it:

```
curl -s -H 'Host: zawiper.example.org' http://127.0.0.1:8460/healthz    # ok
```

Then open the page.  It's an empty wall, waiting for bays.

### The signing key

zaweb makes its signing key (Ed25519) on first start, in `/opt/zawiper/data/keys/`, and publishes the public half at `/za-wiper.pub`.  Every record is signed with it as it arrives.  Before the first real wipe, do three things:

1. **Back up `/opt/zawiper/data`.**  It holds every record and the key, and nothing else can recreate them.
2. **Write the key's fingerprint down somewhere that isn't the hub:** `ssh-keygen -l -f za-wiper.pub`.  An auditor trusts a record because the signature matches a key they got from you, not from the hub that signed it.
3. **Decide who sees records.**  By default nothing is public: a record leaves as print, PDF or `proof.zip`.  `-site-url` puts a link and a QR code on the certificate instead, for a hub people outside can reach.

### Deploying the hub from CI

`.woodpecker/hub.yml` does the steps above on every push to master: test, build the image on the hub host's own Docker, copy the compose file, `docker compose up -d`.  It runs on a Woodpecker agent labelled `zawiper=hub`, and needs the repo marked trusted so it can mount `/opt/zawiper`.  It never touches a bay.

## 3. The first bay

**Build the bundle** on the build machine: `make bundle`.  It runs the tests, then packs `bayd`, `bayd-link`, `bayrules`, the units and the installer into `bin/zawiper-bay-<version>-<commit>.tar.gz` and prints its SHA-256.  Copy it to the dock host and unpack it into `~/zawiper-bay`.

**Find the selector.**  Put a drive in the dock, then:

```
sudo ~/zawiper-bay/bin/bayd ports
```

It lists each USB disk with the port it's on (`2-1`) and its LUN.  A two-slot dock shows LUNs 0 and 1 on one port.  For a SAS HBA port, use `ls -l /dev/disk/by-path/ | grep sas` and take the whole disk's name (`pci-0000:03:00.0-sas-phy2-lun-0`).

**Write its config** as `site/bays/<host>/<name>.toml`, named after the bay:

```
name = "bay1"
usb_port = "2-1"            # or: sas_path = "pci-0000:03:00.0-sas-phy2-lun-0"
usb_lun = "0"               # one slot of a multi-slot dock; leave it out for a single-slot one
smart_type = "sat"          # sat for USB-SATA; scsi for SAS
caps = "wipe"               # leave caps out and the bay only reads
```

**Mint its certificate,** on the CA machine: `deploy/ca.sh bay lab-a bay1`.  Copy `zawiper-certs/lab-a/{ca.crt,bay1.crt,bay1.key}` to `/etc/zawiper/tls/` on the dock host: the key `root:root 0600`, the certificates `0644`.  Then delete the key from the CA machine.  The bay has it, and a bay key is what lets something speak to the hub as that bay.

**Install,** on the dock host, with the config copied to `/etc/zawiper/bays/<name>.toml` (root-owned):

```
sudo ~/zawiper-bay/deploy/install-bay.sh --hub zawiper.example.org:8461
```

Or, from the build machine, the config and the install in one go:

```
make bay-config bay HOST=lab-a
```

`bay-config` checks the host's configs with bayd's own parser, then copies them to `/etc/zawiper/bays/`.  `bay` builds the bundle, copies it over and runs the installer with `HUB` from `site/site.env`.  The hub address goes in `/etc/zawiper/link.env` the first time; later installs keep it.  The bay shows up on the wall.

## 4. Prove the scope before you trust it

The safety of the whole system rests on each bay being able to open its own drive and nothing else.  Look before you wipe anything:

```
ls -l /dev/zawiper/                                    # bay1 -> ../sdc, bay1-sg -> ../sg2
systemctl show bayd@bay1 -p DevicePolicy -p DeviceAllow
```

`DevicePolicy=closed`, and every `DeviceAllow=` entry is a `/dev/zawiper/bay1…` node.  Nothing else.  To see the filter work, try to read another disk under the same rules:

```
sudo systemd-run --wait --pipe -p DevicePolicy=closed -p 'DeviceAllow=/dev/zawiper/bay1 rw' \
    dd if=/dev/sda of=/dev/null bs=4k count=1          # Operation not permitted
```

## 5. The first wipe

Use a drive you don't care about.

1. Put it in.  The bay reads it: identity, SMART, partitions, how much file data they hold.  If it refuses, the refusal says why.
2. Press **Wipe**, check the confirmation names the drive you meant, and confirm.
3. Watch the surface map fill: written, then verified.  A terabyte takes several hours.  Nothing on the page can stop it.  Stopping takes root on the dock host (`systemctl stop bayd@bay1`), and the job comes back INTERRUPTED.
4. When it's done, open the record from the drive's page and download its `proof.zip`.

**Check the record the way an auditor would,** on another machine, with nothing from the hub but the zip and the fingerprint you wrote down.  `record.md` inside it lists the exact commands:

- **The signature,** with OpenSSH alone: an `allowed_signers` line, then `ssh-keygen -Y verify`.
- **The wipe itself:** the record publishes the keystream's key, so `zverify` (or `openssl` and `cmp`) can regenerate what was written and compare it with the drive.

If both pass, the system works end to end.

## 6. More bays

Repeat §3 and §4 for each one.  A dock host can hold many bays, one per slot or port.  Two bays that could reach the same drive fail the install before it changes anything.

## 7. Keeping it running

- **Bay certificates last a year.**  Put the renewal on a calendar.  Re-mint with `ca.sh bay`, copy the new pair in, and restart `bayd-link@<bay>`; that's safe even mid-wipe.
- **Updating a dock host** is the same `make bay HOST=` or installer run.  It never restarts a running `bayd@`, which might be mid-wipe.  It tells you which ones to restart once their bays are idle.
- **Updating the hub** is `make image` and `docker compose up -d`, or a push, if CI deploys it.  A running wipe doesn't need the hub.  Records wait on the dock host until the hub is back.
- **Back up `/opt/zawiper/data`.**  Then restore it somewhere and open a record from the copy.  A backup is not a backup until it has been restored.

## Not built yet

Sign-in, groups that control a bay, custody fields and hash-chained records are designed for institutional use (spec §2, §10), but not built.  Until they are, anyone who can reach the page can start a wipe in a bay that has `caps = "wipe"`.  Keep the page on a network where that's true only of the people who should.
