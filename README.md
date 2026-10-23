# Za Wiper

The drive bay from [docs/za-wiper.md](../docs/za-wiper.md).  Phase 1: put a drive in and its facts appear.  Nothing on the page can write to a drive yet.

- **`bayd`** runs as root under systemd on dock-host.  It watches one USB port, pinned in `/etc/zawiper/bay.toml`.  When a drive appears there, it sets the device read-only, reads SMART, probes signatures, and mounts each filesystem read-only in a private namespace to measure it.  Then it files a record under `/srv/zawiper/drives/<serial>/`.  It answers on `/run/zawiper/bayd.sock`, which takes two verbs, `status` and `reread`.  Neither verb takes a device.
- **`zaweb`** is an unprivileged scratch container on `:8460`.  It talks to `bayd` over the socket and reads records from a read-only mount.

## Work on it

```
make test
make build
truncate -s 1000204886016 /tmp/bay.img          # a "drive"; delete it to pull it
bin/bayd -dev-bay /tmp/bay.img -dev-smart internal/smart/testdata/st31000528as.json \
         -socket $XDG_RUNTIME_DIR/zw.sock -records /tmp/zw
bin/zaweb -listen 127.0.0.1:8461 -socket $XDG_RUNTIME_DIR/zw.sock -records /tmp/zw -hosts 127.0.0.1:8461
```

## Deploy

`make deploy` tests, builds, copies `bin/` and `deploy/` to dock-host, and runs `deploy/install.sh`.  The first time, provision the bay by hand: `sudo bayd ports`, then write `/etc/zawiper/bay.toml`:

```
name = "the bay"
port = "10-1"          # the caddy's USB port, as the VM's sysfs names it
smart_type = "sat"
```
