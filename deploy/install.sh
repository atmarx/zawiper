#!/usr/bin/env bash
# Install or update Za Wiper on dock-host.  Run as root from the zawiper/
# directory, after `make build` (the image copies bin/zaweb).  It never
# writes bay.toml: that file pins the bay, and only a person provisioning
# the bay should write it.
set -euo pipefail
cd "$(dirname "$0")/.."

install -m 0755 bin/bayd bin/zwipe /usr/local/bin/
install -d -m 0755 /etc/zawiper /srv/zawiper
if [[ ! -f /etc/zawiper/bay.toml ]]; then
    echo "No /etc/zawiper/bay.toml.  Run \`bayd ports\`, then write it:" >&2
    echo '  name = "the bay"' >&2
    echo '  port = "10-1"' >&2
    echo '  smart_type = "sat"' >&2
    exit 1
fi
install -m 0644 deploy/bayd.service /etc/systemd/system/bayd.service
systemctl daemon-reload
systemctl enable bayd >/dev/null
systemctl restart bayd

docker compose -f deploy/compose.yml up -d --build
systemctl --no-pager --lines=5 status bayd
