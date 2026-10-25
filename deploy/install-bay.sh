#!/usr/bin/env bash
# Install or update Za Wiper's bays on a dock host (spec §8).  Run as root
# from an unpacked bundle (`make bundle`: bin/ and deploy/ side by side).
#
#   sudo deploy/install-bay.sh [--hub host:port]
#
# It installs bayd and bayd-link, the bayd@ and bayd-link@ template units,
# and udev rules made from /etc/zawiper/bays/*.toml, then enables
# bayd-link@<bay> for each bay.  It also retires Phase 1 (one bayd.service,
# /etc/zawiper/bay.toml, a local zaweb container), as on dock-host.
#
# It never writes a bay config or a key.  Those come from the person
# provisioning the host (zawiper/README.md).  And it never restarts a
# running bayd@: that one might be in the middle of a wipe.
set -euo pipefail
cd "$(dirname "$0")/.."

die() { echo "install-bay: $*" >&2; exit 1; }
say() { echo "install-bay: $*"; }

HUB=""
while [[ $# -gt 0 ]]; do
    case "$1" in
    --hub) HUB="${2:?--hub needs host:port}"; shift 2 ;;
    --hub=*) HUB="${1#--hub=}"; shift ;;
    *) echo "usage: install-bay.sh [--hub host:port]" >&2; exit 2 ;;
    esac
done

[[ $EUID -eq 0 ]] || die "run me as root"
for f in bin/bayd bin/bayd-link bin/bayrules deploy/systemd/bayd@.service \
    deploy/systemd/bayd-link@.service deploy/sysusers.d/zawiper.conf; do
    [[ -f "$f" ]] || die "no $f here.  Run me from an unpacked bundle (make bundle)."
done
# What bayd's read runs, and what this script needs.
for c in smartctl blkid blockdev unshare mount df systemctl udevadm systemd-sysusers; do
    command -v "$c" >/dev/null || die "this host has no $c"
done

BAYS=/etc/zawiper/bays
TLS=/etc/zawiper/tls
shopt -s nullglob

# --- Phase 1, still configured and nothing to replace it -----------------
# bay.toml can't be translated by a script that doesn't write bay configs,
# so say what to write, and stop before changing anything.
configs=("$BAYS"/*.toml)
if [[ -f /etc/zawiper/bay.toml && ${#configs[@]} -eq 0 ]]; then
    port="$(sed -n 's/^[[:space:]]*port[[:space:]]*=[[:space:]]*"\([^"]*\)".*/\1/p' /etc/zawiper/bay.toml)"
    smart="$(sed -n 's/^[[:space:]]*smart_type[[:space:]]*=[[:space:]]*"\([^"]*\)".*/\1/p' /etc/zawiper/bay.toml)"
    host="$(hostname -s)"
    cat >&2 <<EOF
This host still runs Phase 1: /etc/zawiper/bay.toml, and nothing in $BAYS.
I don't write bay configs.  Pick a name for the bay, then write
$BAYS/<name>.toml yourself:

    name = "<name>"
    usb_port = "${port:-?}"
    smart_type = "${smart:-sat}"
    caps = "wipe"

Get its certificate (deploy/ca.sh bay $host <name>) into $TLS/ as
ca.crt, <name>.crt and <name>.key (root, 0600), and run me again.
Nothing has been changed.
EOF
    exit 1
fi

# --- Check the bay configs and make the rules, before touching anything --
gen="$(mktemp -d)"
trap 'rm -rf "$gen"' EXIT
bin/bayrules -bays "$BAYS" -check
bin/bayrules -bays "$BAYS" -out "$gen"
mapfile -t bays <"$gen/bays"

# A Phase 1 wipe runs as a transient unit.  Don't pull bayd out from under it.
if systemctl is-active --quiet zwipe.service; then
    die "zwipe is running (systemctl status zwipe).  Let it finish, then run me again."
fi

# --- Retire Phase 1 -------------------------------------------------------
if [[ -f /etc/systemd/system/bayd.service ]]; then
    say "retiring Phase 1's bayd.service"
    systemctl disable --now bayd.service >/dev/null 2>&1 || true
    rm -f /etc/systemd/system/bayd.service
fi
if command -v docker >/dev/null &&
    [[ "$(docker inspect -f '{{.Config.Image}}' zaweb 2>/dev/null || true)" == zawiper/zaweb:local ]]; then
    say "removing Phase 1's zaweb container; the hub lives on hub-host now"
    docker rm -f zaweb >/dev/null
fi
rm -f /usr/local/bin/zwipe /run/zawiper/bayd.sock
if [[ -f /etc/zawiper/bay.toml ]]; then
    say "moving /etc/zawiper/bay.toml aside to bay.toml.phase1; $BAYS replaces it"
    mv /etc/zawiper/bay.toml /etc/zawiper/bay.toml.phase1
fi

# --- The zawiper user, directories, the sg driver -------------------------
install -m 0644 deploy/sysusers.d/zawiper.conf /etc/sysusers.d/zawiper.conf
systemd-sysusers /etc/sysusers.d/zawiper.conf
install -d -m 0755 /etc/zawiper "$BAYS" "$TLS"
if [[ -n "$HUB" || ! -f /etc/zawiper/link.env ]]; then
    echo "HUB=${HUB:-zawiper.example.org:8461}" >/etc/zawiper/link.env
fi
say "links dial $(sed -n 's/^HUB=//p' /etc/zawiper/link.env)"
# The -sg node needs the sg driver, which isn't always loaded.
echo sg >/etc/modules-load.d/zawiper.conf
modprobe sg || say "couldn't load sg; /dev/zawiper/<bay>-sg won't appear"

# --- Binaries, units, rules -----------------------------------------------
install -m 0755 bin/bayd bin/bayd-link /usr/local/bin/
install -m 0644 deploy/systemd/bayd@.service deploy/systemd/bayd-link@.service /etc/systemd/system/
# The device drop-ins are all generated: clear the old ones, then lay down
# the new.  A bay that's gone loses its binding with its config.
for d in /etc/systemd/system/bayd@*.service.d; do
    rm -f "$d/10-zawiper-device.conf"
    rmdir --ignore-fail-on-non-empty "$d"
done
cp -r "$gen"/units/. /etc/systemd/system/
install -m 0644 "$gen/61-zawiper.rules" /etc/udev/rules.d/61-zawiper.rules
# udev's runtime DeviceAllow= drop-ins restate the template's list, so they
# go stale when the template changes.  Clear them for any bayd@ that isn't
# running; the udev trigger below adds back whatever is in a bay now.
for f in /run/systemd/system.control/bayd@*.service.d/50-DeviceAllow.conf; do
    unit="$(basename "$(dirname "$f")" .d)"
    systemctl is-active --quiet "$unit" || rm -f "$f"
done
systemctl daemon-reload
udevadm control --reload

# --- Links ------------------------------------------------------------------
is_bay() {
    local b
    for b in "${bays[@]}"; do [[ "$b" == "$1" ]] && return 0; done
    return 1
}
# Links for bays that have no config any more.
for w in /etc/systemd/system/multi-user.target.wants/bayd-link@*.service; do
    unit="$(basename "$w")"
    b="${unit#bayd-link@}"
    b="${b%.service}"
    if ! is_bay "$b"; then
        say "bay $b has no config now; disabling its link"
        systemctl disable --now "$unit" >/dev/null 2>&1 || true
    fi
done
for b in "${bays[@]}"; do
    missing=()
    for f in "$TLS/ca.crt" "$TLS/$b.crt" "$TLS/$b.key"; do
        [[ -f "$f" ]] || missing+=("$f")
    done
    if [[ ${#missing[@]} -gt 0 ]]; then
        say "bay $b: no ${missing[*]}.  Its link stays off until the certificate is in place."
        systemctl disable --now "bayd-link@$b.service" >/dev/null 2>&1 || true
        continue
    fi
    perm="$(stat -c '%U %a' "$TLS/$b.key")"
    if [[ "$perm" != "root 600" && "$perm" != "root 400" ]]; then
        say "bay $b: $TLS/$b.key is \"$perm\"; it should be root and 0600.  Fix it; I don't touch keys."
    fi
    systemctl enable --quiet "bayd-link@$b.service"
    # A link restart is safe: a running job needs nothing from it.
    systemctl restart "bayd-link@$b.service"
done

# --- Disk sides ---------------------------------------------------------------
# Re-run udev over the disks so the new rules take: /dev/zawiper/<bay>
# appears for a drive already in a bay, and udev starts its bayd@.
udevadm trigger --action=change --subsystem-match=block --subsystem-match=scsi_generic
udevadm settle --timeout=30 || true
for b in "${bays[@]}"; do
    if systemctl is-active --quiet "bayd@$b.service"; then
        say "bayd@$b is running and may be mid-job, so I left it alone.  When the bay is idle:  systemctl restart bayd@$b"
    elif [[ -e "/dev/zawiper/$b" ]]; then
        systemctl start "bayd@$b.service"
    fi
done
while read -r u _; do
    b="${u#bayd@}"
    b="${b%.service}"
    is_bay "$b" || say "$u is still running, but bay $b has no config now.  Stop it when it's idle."
done < <(systemctl list-units --plain --no-legend --state=active 'bayd@*.service')

if [[ -d /srv/zawiper/drives ]]; then
    say "Phase 1 records are still in /srv/zawiper.  Copy them to the hub (hub-host:/opt/zawiper/data/) and then remove them; see zawiper/README.md."
fi

systemctl --no-pager list-units --all 'bayd@*' 'bayd-link@*'
