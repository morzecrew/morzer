#!/usr/bin/env bash
# Re-runs the measurement behind RFC 0023 §12 item 4.
set -euo pipefail

cd "$(dirname "$0")"
name="morzer-envfile-spike"

cleanup() { docker rm -f "$name" >/dev/null 2>&1 || true; }
trap cleanup EXIT

echo "== building the venue =="
docker build -q -t "$name:venue" -f Dockerfile . >/dev/null
docker build -q -t "$name:units" -f units.Dockerfile . >/dev/null

echo "== booting =="
cleanup
docker run -d --name "$name" --privileged \
    --tmpfs /run --tmpfs /run/lock \
    -v /sys/fs/cgroup:/sys/fs/cgroup:rw --cgroupns=host \
    "$name:units" >/dev/null

for _ in $(seq 1 30); do
    case "$(docker exec "$name" systemctl is-system-running 2>/dev/null)" in
        running|degraded) break ;;
    esac
    sleep 2
done

echo
echo "== /run is a tmpfs =="
docker exec "$name" findmnt -no FSTYPE,OPTIONS /run

echo
echo "== outcomes =="
printf '%-14s %-9s %s\n' unit active result
for u in demo-render demo-a demo-b demo-c; do
    printf '%-14s %-9s %s\n' "$u" \
        "$(docker exec "$name" systemctl is-active "$u.service" || true)" \
        "$(docker exec "$name" systemctl show "$u.service" -p Result --value)"
done

echo
echo "== what they printed =="
docker exec "$name" journalctl -b --no-pager -o cat \
    -u demo-a.service -u demo-b.service -u demo-c.service \
    | grep -E "_PORT=|Failed to load"

echo
echo "== ordering =="
docker exec "$name" journalctl -b --no-pager -o short-precise \
    -u demo-render.service -u demo-b.service -u demo-c.service \
    | grep -E "Starting|Finished" \
    | sed -E 's/^([A-Za-z]+ [0-9]+ [0-9:.]+) [^ ]+ systemd\[1\]: /\1  /'
