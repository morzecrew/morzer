#!/bin/bash
# Re-runs the measurements behind RFC 0055 §10, S1–S6, on an Apple-silicon Mac
# with Docker Desktop. Run each phase as root, `sudo ./run.sh <phase>`; README.md
# says what each one answers and the order to run them in. Every phase appends
# what it saw to report.txt beside this file.
#
# /bin/bash on macOS is 3.2, and the userland is BSD, so nothing here needs a
# newer bash, GNU stat or GNU date.
set -u

p=morzer-spike
etc=/etc/$p
lib=/var/lib/$p
opt=/opt/$p
run=/var/run/$p
here=$(cd "$(dirname "$0")" && pwd)
src=$(cd "$here/../.." && pwd)
report=$here/report.txt
# The helper image the manager already pulls (DefaultHelperImage,
# internal/adapters/runtime/compose/volumes.go). Its index covers linux/arm64.
image=busybox@sha256:9db7b59979c38555a39def84a31fb98b5296952f9e3afd4f6f11f05b07adfab0
# go.mod's version, pinned, so the darwin binary is cross-compiled on Linux the
# way goreleaser builds a release.
golang=golang:1.26.0@sha256:fb612b7831d53a89cbc0aaa7855b69ad7b0caf603715860cf538df854d047b84
label=dev.morzer.spike
daemons=/Library/LaunchDaemons
# launchd starts a daemon with this PATH, and a manager under it sees no other.
launchd_path=/usr/bin:/bin:/usr/sbin:/sbin

now() { date -u +%Y-%m-%dT%H:%M:%SZ; }
hdr() { printf '\n== %s ==\n' "$*"; }
row() { printf '%-36s %s\n' "$1" "$2"; }
# One line of a command's output, for a row: Compose's progress lines dropped.
oneline() { grep -v -E '^ *(Network|Container|Volume) ' | tr '\n' ' ' | cut -c1-240; }
# A command's output on one line, then its exit status.
try() {
    local o r
    o=$("$@" 2>&1)
    r=$?
    printf '%s(exit %s)' "$(printf '%s\n' "$o" | oneline)" "$r"
}

die() { echo "run.sh: $*" >&2; exit 1; }
[ "$(uname -s)" = Darwin ] || die "this runs on a Mac"
[ "$(id -u)" = 0 ] || die "run it as root: sudo $0 ${1:-all}"
user=${SUDO_USER:-}
[ -n "$user" ] && [ "$user" != root ] || die "run it with sudo from your own account, not from a root shell"
home=$(dscl . -read "/Users/$user" NFSHomeDirectory | awk '{print $2}')
uid=$(id -u "$user")
gid=$(id -g "$user")

docker_bin=
for c in /usr/local/bin/docker "$home/.docker/bin/docker" /Applications/Docker.app/Contents/Resources/bin/docker; do
    if [ -x "$c" ]; then docker_bin=$c; break; fi
done
[ -n "$docker_bin" ] || die "no docker CLI found; is Docker Desktop installed?"

# Root as launchd starts it: root's own HOME, launchd's PATH, nothing of the
# user's environment. This is how the manager will run.
rootdocker() { env -i PATH="$launchd_path" HOME=/var/root "$docker_bin" "$@"; }
# The user's own CLI, with the user's config and context.
userdocker() { sudo -u "$user" -H "$docker_bin" "$@"; }

# S1, S2 and S6 need an engine. They use root's, as the manager would, and fall
# back to the user's only so that a failed S3 does not hide the other answers.
via=
pick_engine() {
    if rootdocker info >/dev/null 2>&1 && rootdocker compose version >/dev/null 2>&1; then
        via=root
    elif userdocker info >/dev/null 2>&1; then
        via=user
    else
        die "no engine answers, as root or as $user; start Docker Desktop and run this again"
    fi
}
engine() { if [ "$via" = root ]; then rootdocker "$@"; else userdocker "$@"; fi; }

tmp=$(mktemp -d /private/tmp/$p.XXXXXX)
# Compose files sit here, so it must be readable by the user's CLI too.
chmod 0755 "$tmp"
trap 'rm -rf "${tmp:?}"' EXIT

touch "$report"
chown "$user" "$report"
exec > >(tee -a "$report") 2>&1

info() {
    hdr "info, $(now)"
    row "macOS" "$(sw_vers -productVersion) ($(sw_vers -buildVersion))"
    row "model" "$(sysctl -n hw.model), $(sysctl -n machdep.cpu.brand_string)"
    row "arch" "$(uname -m)"
    row "FileVault" "$(fdesetup status | head -1)"
    row "SIP" "$(csrutil status 2>&1 | head -1)"
    row "Docker Desktop" "$(plutil -extract CFBundleShortVersionString raw -o - /Applications/Docker.app/Contents/Info.plist 2>&1)"
    row "docker CLI" "$docker_bin -> $(readlink "$docker_bin" || echo 'not a link')"
    row "engine, as $user" "$(userdocker version --format '{{.Server.Platform.Name}}, engine {{.Server.Version}}' 2>&1 | oneline)"
    row "compose, as $user" "$(userdocker compose version --short 2>&1 | oneline)"
    for f in "$home/Library/Group Containers/group.com.docker/settings-store.json" \
        "$home/Library/Group Containers/group.com.docker/settings.json"; do
        [ -f "$f" ] || continue
        row "settings" "$f"
        grep -o -i -E '"[A-Za-z]*(socket|virtiofs|virtualization|grpcfuse|autostart|filesharing)[A-Za-z]*" *: *("[^"]*"|\[[^]]*\]|[a-z0-9]+)' "$f" |
            sed 's/^/    /'
    done
}

s3() {
    hdr "S3, root's docker and Docker Desktop, $(now)"
    row "/var/run/docker.sock" "$(ls -l /var/run/docker.sock 2>&1)"
    row "what it reaches" "$(ls -lL /var/run/docker.sock 2>&1)"
    row "$home/.docker/run/docker.sock" "$(ls -l "$home/.docker/run/docker.sock" 2>&1)"
    for c in /usr/local/bin/docker "$home/.docker/bin/docker"; do
        row "$c" "$(ls -l "$c" 2>&1)"
    done
    row "docker on launchd's PATH" "$(env -i PATH="$launchd_path" /bin/sh -c 'command -v docker' || echo "none on $launchd_path")"
    row "compose plugin dirs" "$(for d in /usr/local/lib/docker/cli-plugins /usr/local/libexec/docker/cli-plugins "$home/.docker/cli-plugins"; do [ -d "$d" ] && printf '%s ' "$d"; done)"
    row "$user's context" "$(userdocker context show 2>&1 | oneline)"
    row "HOME under sudo" "$HOME"
    row "root, sudo's env: version" "$(try "$docker_bin" version --format '{{.Server.Version}}')"
    row "root, clean env: version" "$(try rootdocker version --format '{{.Server.Version}}')"
    row "root, clean env: compose" "$(try rootdocker compose version --short)"
    row "root, $user's socket" "$(try rootdocker -H "unix://$home/.docker/run/docker.sock" version --format '{{.Server.Version}}')"
    pick_engine
    row "S1, S2 and S6 run as" "$via"
}

s1() {
    hdr "S1, which host paths a bind shares, by spelling, $(now)"
    pick_engine
    row "engine as" "$via"
    engine pull -q "$image" >/dev/null 2>&1
    for d in "$etc" "$lib" "$opt" "$run"; do
        if out=$(mkdir -p "$d" 2>&1); then
            echo probe >"$d/probe"
            row "mkdir $d" "ok"
        else
            row "mkdir $d" "FAILED: $out"
        fi
    done
    for d in /etc /var/lib /opt /var/run; do
        row "$d" "$(ls -ld "$d" 2>&1) -> $(cd "$d" 2>/dev/null && pwd -P)"
    done
    for s in "/etc/$p" "/private/etc/$p" "/var/lib/$p" "/private/var/lib/$p" \
        "/opt/$p" "/var/run/$p" "/private/var/run/$p"; do
        cat >"$tmp/s1.yaml" <<EOF
services:
  probe:
    image: $image
    volumes:
      - $s:/mnt/probe
    command: ["sh", "-c", "cat /mnt/probe/probe && touch /mnt/probe/written && echo wrote"]
EOF
        out=$(engine compose -p "$p-s1" -f "$tmp/s1.yaml" run --rm -T probe 2>&1)
        rc=$?
        owner=$(stat -f '%u:%g' "$s/written" 2>/dev/null || echo -)
        rm -f "$s/written"
        row "$s" "exit $rc, a file the container wrote is owned by $owner on the Mac | $(printf '%s' "$out" | oneline)"
    done
    engine compose -p "$p-s1" -f "$tmp/s1.yaml" down >/dev/null 2>&1
}

# s2_variant NAME DIR DIRMODE FILEMODE FILEOWNER
s2_variant() {
    name=$1 dir=$2
    mkdir -p "$dir"
    chown 0:0 "$dir"
    chmod "$3" "$dir"
    printf 'token\n' >"$dir/token"
    chown "$5" "$dir/token"
    chmod "$4" "$dir/token"
    # \$\$ reaches Compose as $$, which it passes to sh as one $.
    cat >"$tmp/s2.yaml" <<EOF
services:
  read:
    image: $image
    secrets: [token]
    command: ["sh", "-c", "printf 'sees %s, ' \"\$\$(stat -c '%u:%g %a' /run/secrets/token 2>&1)\"; cat /run/secrets/token >/dev/null 2>&1 && echo READ || echo DENIED"]
secrets:
  token:
    file: $dir/token
EOF
    echo "$name: dir $3 root, file $4 $5, at $dir/token"
    for u in 65532 0 1000; do
        out=$(engine compose -p "$p-s2" -f "$tmp/s2.yaml" run --rm -T --user "$u" read 2>&1)
        row "    as uid $u" "exit $?: $(printf '%s' "$out" | oneline)"
    done
}

s2() {
    hdr "S2, what owner a container sees for a rendered secret, $(now)"
    pick_engine
    row "engine as" "$via"
    engine pull -q "$image" >/dev/null 2>&1
    # The real layout first: RunDir/secrets is 0700 root, and a secret declaring
    # owner 65532 is 0400 65532:65532. The rest take one condition away at a
    # time, so a DENIED can be told apart from a uid mapping.
    s2_variant "layout" "/private/var/run/$p/secrets" 0700 0400 65532:65532
    s2_variant "layout, under /tmp" "/private/tmp/$p-s2/layout" 0700 0400 65532:65532
    s2_variant "open dir" "/private/tmp/$p-s2/open" 0755 0400 65532:65532
    s2_variant "readable" "/private/tmp/$p-s2/readable" 0755 0444 65532:65532
    s2_variant "owned by $user" "/private/tmp/$p-s2/user" 0755 0400 "$uid:$gid"
    engine compose -p "$p-s2" -f "$tmp/s2.yaml" down >/dev/null 2>&1
    rm -rf "/private/tmp/${p:?}-s2"
}

s6() {
    hdr "S6, a cross-compiled darwin/arm64 binary from curl, $(now)"
    pick_engine
    out=/private/tmp/$p-s6
    bin=$lib/bin
    rm -rf "${out:?}"
    mkdir -p "$out" "$bin"
    chmod 0777 "$out"
    # Built in a Linux container: the cross-compile goreleaser does, with its
    # flags. Two versions, so a replacement can be told from the original.
    engine run --rm -v "$src:/src:ro" -v "$out:/out" -v "$p-gocache:/root/go" -w /src \
        -e CGO_ENABLED=0 -e GOOS=darwin -e GOARCH=arm64 -e GOFLAGS=-buildvcs=false "$golang" sh -c '
            go version &&
            go build -trimpath -ldflags "-s -w -X main.version=0.0.0-spike.old" -o /out/old ./cmd/morzer &&
            go build -trimpath -ldflags "-s -w -X main.version=0.0.0-spike.new" -o /out/new ./cmd/morzer' 2>&1 |
        tail -3
    [ -f "$out/old" ] || { row "build" "FAILED"; return; }

    # Served over HTTP and fetched with curl, as install.sh fetches an archive.
    engine run -d --name "$p-s6-http" -p 127.0.0.1::8080 -v "$out:/www:ro" "$image" \
        httpd -f -p 8080 -h /www >/dev/null 2>&1
    port=$(engine port "$p-s6-http" 8080 2>/dev/null | head -1 | sed 's/.*://')
    sleep 1
    curl -fsS -o "$bin/morzer" "http://127.0.0.1:$port/old" && chmod 0755 "$bin/morzer"
    curl -fsS -o "$bin/.morzer.staged" "http://127.0.0.1:$port/new" && chmod 0755 "$bin/.morzer.staged"
    engine rm -f "$p-s6-http" >/dev/null 2>&1

    row "xattr after curl" "$(xattr -l "$bin/morzer" 2>&1 | oneline)(empty is no quarantine)"
    row "codesign" "$(codesign -dv "$bin/morzer" 2>&1 | grep -E 'Signature|Identifier|CodeDirectory' | oneline)"
    row "spctl" "$(spctl --assess --type execute -v "$bin/morzer" 2>&1 | oneline)"
    row "runs" "$(try "$bin/morzer" version)"

    # What self-update does: the new binary beside the old, one rename over it
    # (internal/lifecycle/ops/self_update.go).
    old_inode=$(stat -f %i "$bin/morzer")
    mv -f "$bin/.morzer.staged" "$bin/morzer"
    row "after a rename over it" "$(try "$bin/morzer" version) (inode $old_inode -> $(stat -f %i "$bin/morzer"))"

    # The contrast: the same path overwritten in place, one inode. A copy that
    # does this is what self-update must never become.
    cat "$out/old" >"$bin/morzer"
    row "after an overwrite in place" "$(try "$bin/morzer" version) (137 is SIGKILL)"

    # And what a browser download would carry.
    cp "$out/new" "$bin/morzer.quarantined"
    xattr -w com.apple.quarantine "0081;$(printf %x "$(date +%s)");Safari;" "$bin/morzer.quarantined"
    row "with a quarantine attribute" "$(try "$bin/morzer.quarantined" version)"
    rm -rf "${out:?}"
}

boot_job() {
    cat <<EOF
#!/bin/sh
# Stands in for \`apply --startup\` under launchd (RFC 0055 §5.6): exit 7 while
# the engine does not answer, exit 12 once when told to, and exit 0 while the
# exit-12 record stands. Once converged, it starts an overdue backup and waits
# for it, read from launchctl print.
log=$lib/logs/attempts.log
now() { date -u +%Y-%m-%dT%H:%M:%SZ; }
if [ -e $lib/exit12.record ]; then
    echo "\$(now) record stands: exit 0 without converging" >>"\$log"
    exit 0
fi
echo "\$(now) attempt, uid \$(id -u), HOME=\${HOME:-unset}, PATH=\$PATH, docker on PATH: \$(command -v docker || echo none), sock: \$(ls -l /var/run/docker.sock 2>&1)" >>"\$log"
if ! out=\$($docker_bin info --format '{{.ServerVersion}}' 2>&1); then
    echo "\$(now) engine does not answer: \$(echo "\$out" | tail -1); exit 7" >>"\$log"
    exit 7
fi
if [ -e $lib/fail12 ]; then
    rm -f $lib/fail12
    touch $lib/exit12.record
    echo "\$(now) engine \$out; exit 12, recorded" >>"\$log"
    exit 12
fi
echo "\$(now) engine \$out; converged" >>"\$log"
if [ ! -e $lib/backup.stamp ]; then
    echo "\$(now) backup overdue: kickstart" >>"\$log"
    /bin/launchctl kickstart system/$label-backup >>"\$log" 2>&1
    i=0
    while [ \$i -lt 60 ] && [ ! -e $lib/backup.stamp ]; do sleep 1; i=\$((i + 1)); done
    sleep 2
    echo "\$(now) backup after \${i}s: \$(/bin/launchctl print system/$label-backup | grep -E '^[[:space:]]*(state|runs|last exit code) =' | tr -s ' \t' ' ' | tr '\n' ';')" >>"\$log"
fi
exit 0
EOF
}

backup_job() {
    cat <<EOF
#!/bin/sh
# Stands in for the backup a timer runs: it logs, takes a few seconds, stamps.
echo "\$(date -u +%Y-%m-%dT%H:%M:%SZ) backup ran, pid \$\$" >>$lib/logs/backup.log
sleep 3
touch $lib/backup.stamp
EOF
}

plist() { # LABEL SCRIPT EXTRA
    cat <<EOF
<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0">
<dict>
    <key>Label</key><string>$1</string>
    <key>ProgramArguments</key><array><string>/bin/sh</string><string>$2</string></array>
    <key>StandardOutPath</key><string>$lib/logs/$1.out</string>
    <key>StandardErrorPath</key><string>$lib/logs/$1.err</string>
$3
</dict>
</plist>
EOF
}

attempts_since() { awk -v t="$1" '$1 >= t' "$lib/logs/attempts.log" 2>/dev/null; }
pause() { printf '\n>>> %s\n>>> Press Enter when done. ' "$1"; read -r _ </dev/tty; }

s4() {
    hdr "S4, the boot job under launchd, $(now)"
    mkdir -p "$lib/logs"
    boot_job >"$lib/boot.sh"
    backup_job >"$lib/backup.sh"
    rm -f "$lib/exit12.record" "$lib/fail12" "$lib/backup.stamp"
    plist "$label" "$lib/boot.sh" '    <key>RunAtLoad</key><true/>
    <key>KeepAlive</key><dict><key>SuccessfulExit</key><false/></dict>
    <key>ThrottleInterval</key><integer>30</integer>' >"$daemons/$label.plist"
    plist "$label-backup" "$lib/backup.sh" '    <key>StartCalendarInterval</key><dict><key>Hour</key><integer>3</integer><key>Minute</key><integer>0</integer></dict>' \
        >"$daemons/$label-backup.plist"
    chown root:wheel "$daemons/$label.plist" "$daemons/$label-backup.plist"
    chmod 0644 "$daemons/$label.plist" "$daemons/$label-backup.plist"
    launchctl bootout "system/$label" 2>/dev/null
    launchctl bootout "system/$label-backup" 2>/dev/null

    echo "a: loaded with the engine running; expect one attempt that converges and starts the overdue backup"
    t=$(now)
    launchctl bootstrap system "$daemons/$label-backup.plist"
    launchctl bootstrap system "$daemons/$label.plist"
    sleep 20
    attempts_since "$t"
    row "backup.log" "$(oneline 2>/dev/null <"$lib/logs/backup.log")"

    pause "Quit Docker Desktop (whale menu > Quit Docker Desktop) and wait until it has quit."
    echo "b: retried while the engine is down; expect an attempt about every 30 seconds, each exit 7"
    t=$(now)
    launchctl kickstart -k "system/$label"
    sleep 95
    attempts_since "$t"

    pause "Start Docker Desktop and wait until it says the engine is running."
    echo "c: expect the next attempt to converge, and the attempts to stop"
    t=$(now)
    i=0
    while [ $i -lt 180 ] && ! attempts_since "$t" | grep -q converged; do sleep 5; i=$((i + 5)); done
    sleep 35
    attempts_since "$t"

    echo "d: an exit 12; expect it once, then one 'record stands' 30 seconds later, then nothing"
    touch "$lib/fail12"
    t=$(now)
    launchctl kickstart -k "system/$label"
    sleep 75
    attempts_since "$t"
    row "launchctl print" "$(launchctl print "system/$label" | grep -E '^[[:space:]]*(state|runs|last exit code) =' | tr -s ' \t' ' ' | tr '\n' ';')"
    rm -f "$lib/exit12.record"
    echo "The two jobs stay loaded for the reboot; next: sudo $0 before-reboot"
}

before_reboot() {
    hdr "before the reboot, $(now)"
    mkdir -p "$run/s5"
    echo "written $(now)" >"$run/s5/marker"
    echo "written $(now)" >"/private/var/run/$p.s5"
    rm -f "$lib/backup.stamp"
    now >"$lib/reboot.mark"
    row "boot time now" "$(sysctl -n kern.boottime)"
    row "S5 markers" "$(cat "$run/s5/marker" "/private/var/run/$p.s5" 2>&1 | oneline)"
    row "S4 jobs loaded" "$(launchctl list | grep -c "$label") of 2"
    row "backup" "stamp removed, so the boot job finds it overdue"
    cat <<EOF

Next, with the power connected and the lid open:
  1. Make sure Docker Desktop starts when you log in (Settings > General).
  2. Restart from the Apple menu.
  3. At the login screen, wait two full minutes by a clock before logging in,
     and note the time you log in.
  4. Run: sudo $0 after-reboot
EOF
}

after_reboot() {
    hdr "after the reboot, $(now)"
    row "boot time now" "$(sysctl -n kern.boottime)"
    row "FileVault" "$(fdesetup status | head -1)"
    row "S5, $run/s5/marker" "$(cat "$run/s5/marker" 2>&1)"
    row "S5, /private/var/run/$p.s5" "$(cat "/private/var/run/$p.s5" 2>&1)"
    row "S5, $run" "$(find "$run" 2>&1 | oneline)"
    row "logins" ""
    last -5 "$user" | sed 's/^/    /'
    printf 'What time did you log in (HH:MM, local)? '
    read -r login </dev/tty
    row "logged in at, by the clock" "$login, $(date +%Z)"
    echo "attempts since the reboot (UTC):"
    attempts_since "$(cat "$lib/reboot.mark" 2>/dev/null || echo 0)"
    row "backup.log" "$(oneline 2>/dev/null <"$lib/logs/backup.log")"
    row "launchctl print" "$(launchctl print "system/$label" 2>&1 | grep -E '^[[:space:]]*(state|runs|last exit code) =' | tr -s ' \t' ' ' | tr '\n' ';')"
    echo "Next: sudo $0 clean, then send report.txt"
}

clean() {
    hdr "clean, $(now)"
    launchctl bootout "system/$label" 2>/dev/null
    launchctl bootout "system/$label-backup" 2>/dev/null
    rm -f "$daemons/$label.plist" "$daemons/$label-backup.plist"
    for d in "$etc" "$lib" "$opt" "$run" "/private/tmp/$p-s2" "/private/tmp/$p-s6"; do
        rm -rf "${d:?}"
    done
    rm -f "/private/var/run/$p.s5"
    userdocker rm -f "$p-s6-http" >/dev/null 2>&1
    userdocker volume rm "$p-gocache" >/dev/null 2>&1
    echo "removed the jobs, $etc, $lib, $opt, $run and the build cache volume; report.txt stays"
}

case "${1:-all}" in
all) info; s3; s1; s2; s6 ;;
info) info ;;
s1) s1 ;;
s2) s2 ;;
s3) s3 ;;
s4) s4 ;;
s6) s6 ;;
before-reboot) before_reboot ;;
after-reboot) after_reboot ;;
clean) clean ;;
*) die "unknown phase $1; see README.md" ;;
esac
