#!/bin/bash
# What launchctl shows around a kickstart (RFC 0055 P2). One throwaway job,
# removed on exit. Run from the directory to write launchd-probe.txt into:
#
#     sudo bash launchd-probe.sh
set -u
L=dev.morzer.probe-catchup
P=/Library/LaunchDaemons/$L.plist
OUT=$PWD/launchd-probe.txt

cleanup() { launchctl bootout "system/$L" 2>/dev/null; rm -f "$P"; }
trap cleanup EXIT
cleanup

cat >"$P" <<PLIST
<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0">
<dict>
	<key>Label</key>
	<string>$L</string>
	<key>ProgramArguments</key>
	<array>
		<string>/bin/sh</string>
		<string>-c</string>
		<string>sleep 15; exit 3</string>
	</array>
	<key>StartCalendarInterval</key>
	<dict>
		<key>Month</key>
		<integer>1</integer>
		<key>Day</key>
		<integer>1</integer>
		<key>Hour</key>
		<integer>4</integer>
		<key>Minute</key>
		<integer>4</integer>
	</dict>
</dict>
</plist>
PLIST
chown root:wheel "$P"; chmod 644 "$P"

log() { printf '\n== %s ==\n' "$*" >>"$OUT"; }
run() { log "$*"; "$@" >>"$OUT" 2>&1; echo "exit $?" >>"$OUT"; }
props() { launchctl print "system/$L" | cat -et | grep '^\^I[a-z]' >>"$OUT"; }

: >"$OUT"
sw_vers >>"$OUT"
run launchctl print system/dev.morzer.no-such-job
run launchctl bootstrap system "$P"
log "print before the first run, whole"; launchctl print "system/$L" | cat -et >>"$OUT"
run launchctl disable "system/$L"
log "print-disabled, probe disabled"
launchctl print-disabled system | cat -et | grep -e "$L" -e 'disabled services' >>"$OUT"
run launchctl enable "system/$L"
log "print-disabled, probe enabled"; launchctl print-disabled system | cat -et | grep -e "$L" >>"$OUT"
run launchctl kickstart "system/$L"
log "at once after kickstart"; props
sleep 1
log "1 s after kickstart, whole"; launchctl print "system/$L" | cat -et >>"$OUT"
run launchctl kickstart "system/$L"
sleep 2
log "3 s after kickstart"; props
sleep 15
log "after it exited, whole"; launchctl print "system/$L" | cat -et >>"$OUT"
echo "wrote $OUT"
