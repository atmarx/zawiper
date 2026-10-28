#!/usr/bin/env bash
# The Za Wiper CA (spec §11): an offline root made with the step CLI, no
# step-ca server, no intermediate.  The root signs the hub's server
# certificate and each bay's client certificate directly.
#
#   CA_DIR=/path/to/dr-keys/zawiper deploy/ca.sh init
#   CA_DIR=... deploy/ca.sh hub <dns> [extra SAN...]
#   CA_DIR=... deploy/ca.sh bay <host> <name>
#
# CA_DIR is where the root lives, and it has no default: the root key goes
# where you say and nowhere else.  It's created encrypted, and step asks for
# its password each time it signs.  Certificates and their (unencrypted)
# keys go to OUT_DIR, default ./zawiper-certs, one folder per destination:
#
#   hub/          ca.crt hub.crt hub.key   -> the hub host's /opt/zawiper/tls/
#   <host>/       ca.crt <name>.crt <name>.key -> <host>:/etc/zawiper/tls/
#
# Neither directory may be inside a git work tree, so no key gets committed
# by accident.
set -euo pipefail

usage() {
    sed -n '6,8p' "$0" | sed 's/^# *//' >&2
    exit 2
}
die() { echo "ca.sh: $*" >&2; exit 1; }

command -v step >/dev/null || die "needs the step CLI (https://smallstep.com/docs/step-cli)"
[[ -n "${CA_DIR:-}" ]] || die "set CA_DIR to where the root lives.  There's no default on purpose."
OUT_DIR="${OUT_DIR:-./zawiper-certs}"
ROOT_CRT="$CA_DIR/root_ca.crt"
ROOT_KEY="$CA_DIR/root_ca.key"
# A year, per the spec.  Override for a short-lived test cert.
NOT_AFTER="${NOT_AFTER:-8760h}"

# Refuse a directory that git would see.  It needn't exist yet: check the
# nearest parent that does.
not_in_git() {
    local d="$1"
    while [[ ! -d "$d" ]]; do d="$(dirname "$d")"; done
    if git -C "$d" rev-parse --is-inside-work-tree >/dev/null 2>&1; then
        die "$1 is inside a git work tree.  Keys don't go anywhere git can see them."
    fi
}

need_root() {
    [[ -f "$ROOT_CRT" && -f "$ROOT_KEY" ]] || die "no root in $CA_DIR.  Run \`ca.sh init\` first."
}

# A bay name ends up in a CN, a file name and a unit instance; the rule is
# config.ValidName's.  A host name is the dock host's short name.
check_bay() {
    [[ "$1" =~ ^[a-z0-9][a-z0-9_-]{0,31}$ ]] || die "bay \"$1\": use 1-32 of a-z, 0-9, _ and -"
}
check_host() {
    [[ "$1" =~ ^[a-z0-9][a-z0-9.-]{0,62}$ ]] || die "host \"$1\": use its short name, a-z, 0-9, . and -"
}

# Leaf templates: the key may sign, and the certificate is good for one
# side of TLS only.  A bay's has no SANs at all; its identity is its CN.
SERVER_TEMPLATE='{
  "subject": {{ toJson .Subject }},
  "sans": {{ toJson .SANs }},
  "keyUsage": ["digitalSignature"],
  "extKeyUsage": ["serverAuth"]
}'
CLIENT_TEMPLATE='{
  "subject": {{ toJson .Subject }},
  "keyUsage": ["digitalSignature"],
  "extKeyUsage": ["clientAuth"]
}'

# sign <template> <subject> <crt> <key> [step args...]
sign() {
    local template="$1" subject="$2" crt="$3" key="$4"
    shift 4
    local tf rc=0
    tf="$(mktemp)"
    printf '%s\n' "$template" >"$tf"
    step certificate create "$subject" "$crt" "$key" \
        --template "$tf" --ca "$ROOT_CRT" --ca-key "$ROOT_KEY" \
        --not-after "$NOT_AFTER" --no-password --insecure "$@" || rc=$?
    rm -f "$tf"
    [[ $rc -eq 0 ]] || die "step failed (exit $rc)"
    chmod 0600 "$key"
}

cmd="${1:-}"
[[ -n "$cmd" ]] || usage
shift
not_in_git "$CA_DIR"

case "$cmd" in
init)
    [[ $# -eq 0 ]] || usage
    [[ ! -e "$ROOT_KEY" ]] || die "$ROOT_KEY exists.  One root; not overwriting it."
    install -d -m 0700 "$CA_DIR"
    # Ten years, encrypted: step asks for the password.
    step certificate create "Za Wiper Root CA" "$ROOT_CRT" "$ROOT_KEY" \
        --profile root-ca --not-after 87600h
    chmod 0600 "$ROOT_KEY"
    echo "Root in $CA_DIR.  Put it with the DR keys; it never goes on a host."
    step certificate fingerprint "$ROOT_CRT"
    ;;
hub)
    [[ $# -ge 1 ]] || usage
    need_root
    not_in_git "$OUT_DIR"
    dns="$1"
    shift
    # The name the bays dial, then any others they might: the hub host by
    # name and by address, the page's name.
    sans=()
    for s in "$dns" "$@"; do
        [[ " ${sans[*]} " == *" $s "* ]] || sans+=("$s")
    done
    args=()
    for s in "${sans[@]}"; do args+=(--san "$s"); done
    out="$OUT_DIR/hub"
    install -d -m 0700 "$out"
    sign "$SERVER_TEMPLATE" "$dns" "$out/hub.crt" "$out/hub.key" "${args[@]}"
    cp "$ROOT_CRT" "$out/ca.crt"
    echo "Hub certificate for ${sans[*]} in $out."
    echo "On the hub host: /opt/zawiper/tls/, hub.key owned 8460 and mode 0400."
    ;;
bay)
    [[ $# -eq 2 ]] || usage
    need_root
    not_in_git "$OUT_DIR"
    host="$1" name="$2"
    check_host "$host"
    check_bay "$name"
    out="$OUT_DIR/$host"
    install -d -m 0700 "$out"
    sign "$CLIENT_TEMPLATE" "bay/$host/$name" "$out/$name.crt" "$out/$name.key"
    cp "$ROOT_CRT" "$out/ca.crt"
    echo "Bay certificate bay/$host/$name in $out, good for $NOT_AFTER."
    echo "On $host: /etc/zawiper/tls/, $name.key owned root and mode 0600."
    ;;
*)
    usage
    ;;
esac
