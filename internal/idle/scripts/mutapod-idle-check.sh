#!/usr/bin/env bash
set -euo pipefail

lease_dir="/var/lib/mutapod/leases"
now="$(date +%s)"
has_live=0

mkdir -p "$lease_dir"
shopt -s nullglob

for lease in "$lease_dir"/*.lease; do
    expires="$(grep '^expires_unix=' "$lease" | cut -d= -f2 || true)"
    if [[ -z "$expires" ]]; then
        rm -f "$lease"
        continue
    fi
    if (( expires > now )); then
        has_live=1
        break
    fi
    rm -f "$lease"
done

if (( has_live == 0 )); then
    boot_grace_seconds=600
    uptime_seconds="$(cut -d. -f1 /proc/uptime 2>/dev/null || echo 0)"
    if [[ "$uptime_seconds" =~ ^[0-9]+$ ]] && (( uptime_seconds < boot_grace_seconds )); then
        exit 0
    fi
    shutdown -h now
fi
