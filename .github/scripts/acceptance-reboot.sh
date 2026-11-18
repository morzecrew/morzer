#!/usr/bin/env bash
# The acceptance run's second phase on a Mac, after a restart and a login.
#
# The first phase, acceptance.sh under `just acceptance-record`, left an
# installation whose jobs launchd runs, and a backup it made due. This checks
# what the restart did with them (RFC 0055 decision 9, logs/macos-host.md):
#
#   the boot job ran during this boot and last exited 0
#   the journal holds an apply begun after the boot, which succeeded
#   the backup's stamp is newer than the boot, and so is a backup listed
#   the boot job's log names the job it caught up
#
# Then it removes the installation and its jobs, whether or not they did.
#
#   just acceptance-record --after-reboot V   # runs this under sudo
#
set -euo pipefail

REBOOT_DIR="${ACCEPTANCE_REBOOT_DIR:-/private/var/morzer-acceptance-reboot}"
ROOT="${REBOOT_DIR}/root"
MORZER="${REBOOT_DIR}/bin/morzer"
VAR_DIR="${ROOT}/var/lib/demo"
BOOT_JOB=dev.morzer.demo

step() { printf '\n\033[1m== %s ==\033[0m\n' "$*"; }
info() { printf '   %s\n' "$*"; }
fail() {
	printf '\n\033[31mFAIL:\033[0m %s\n' "$*" >&2
	exit 1
}

[ "$(uname -s)" = Darwin ] || fail "this is the second phase of a run on a Mac"
[ "$(id -u)" = 0 ] || fail "run it with sudo, as the first phase was"
[ -x "${MORZER}" ] || fail "no installation from the first phase in ${REBOOT_DIR}"

# remove takes the installation, its jobs and its directory away, as the end of
# the run, whether or not the checks passed: it is the one installation on this
# Mac whose jobs a test left in /Library/LaunchDaemons.
remove() {
	local status=$?
	step "removing the restart's installation and its jobs"
	local id
	id=$("${MORZER}" --root "${ROOT}" --json status 2>/dev/null | jq -r '.data.installation_id // empty' || true)
	if [ -n "${id}" ]; then
		"${MORZER}" --root "${ROOT}" installation remove --force --confirm "${id}" >/dev/null 2>&1 ||
			info "installation remove failed; the jobs below are removed by hand"
	fi
	local plist label
	for plist in /Library/LaunchDaemons/"${BOOT_JOB}".plist /Library/LaunchDaemons/"${BOOT_JOB}"-*.plist; do
		[ -e "${plist}" ] || continue
		label=$(basename "${plist}" .plist)
		launchctl bootout "system/${label}" >/dev/null 2>&1 || true
		rm -f -- "${plist}"
	done
	rm -f -- /etc/newsyslog.d/"${BOOT_JOB}".conf
	case "${REBOOT_DIR}" in
	/private/var/morzer-acceptance-reboot*) rm -rf -- "${REBOOT_DIR}" ;;
	*) info "left ${REBOOT_DIR}, which is not where the first phase installs" ;;
	esac
	return "${status}"
}
trap remove EXIT

BOOT=$(sysctl -n kern.boottime | sed -E 's/^\{ sec = ([0-9]+),.*/\1/')
[ -n "${BOOT}" ] || fail "cannot read when this Mac booted"
info "booted at $(date -r "${BOOT}" -u +%FT%TZ)"

# print reads one of a job's own properties, the lines one tab in.
print() { launchctl print "system/$1" 2>/dev/null | sed -n "s/^	$2 = //p" | head -1; }

step "the boot job ran during this boot and last exited 0"
# launchd counts a job's runs from the boot, so one is a run since it. The boot
# job waits for each caught-up job before it exits, so it may still be running.
for _ in $(seq 1 120); do
	[ "$(print "${BOOT_JOB}" state)" = running ] || break
	sleep 5
done
runs=$(print "${BOOT_JOB}" runs)
code=$(print "${BOOT_JOB}" "last exit code")
info "runs = ${runs:-none}, last exit code = ${code:-none}, state = $(print "${BOOT_JOB}" state)"
[ "${runs:-0}" -ge 1 ] 2>/dev/null || fail "the boot job has not run since the boot"
[ "${code}" = 0 ] || fail "the boot job's last run exited ${code:-without a code}"

step "the journal holds an apply begun after the boot, which succeeded"
history=$("${MORZER}" --root "${ROOT}" --json history --limit 0)
applies=$(printf '%s' "${history}" | jq --argjson boot "${BOOT}" '[.data.operations[]
	| select(.type == "apply" and .status == "succeeded" and (.started_at | fromdateiso8601) > $boot)] | length')
[ "${applies}" -ge 1 ] ||
	fail "no apply begun after the boot succeeded: $(printf '%s' "${history}" | jq -c '[.data.operations[] | {type, status, started_at}]')"
info "${applies} apply(ies) since the boot"

step "the backup the restart missed was caught up"
stamp="${VAR_DIR}/manager/stamps/backup"
[ -f "${stamp}" ] || fail "the backup has no stamp: it never reached the engine"
[ "$(stat -f %m "${stamp}")" -gt "${BOOT}" ] ||
	fail "the backup's stamp, $(date -r "$(stat -f %m "${stamp}")" -u +%FT%TZ), is older than the boot"
backups=$("${MORZER}" --root "${ROOT}" --json backup list)
newer=$(printf '%s' "${backups}" | jq --argjson boot "${BOOT}" '[.data[] | select((.at | fromdateiso8601) > $boot)] | length')
[ "${newer}" -ge 1 ] || fail "no backup was taken since the boot: $(printf '%s' "${backups}" | jq -c '[.data[] | {id, at}]')"
info "${newer} backup(s) since the boot"
log="${VAR_DIR}/logs/${BOOT_JOB}.log"
if ! grep 'caught up' "${log}" 2>/dev/null | grep -q "${BOOT_JOB}-backup"; then
	fail "the boot job's log does not name the backup as caught up: $(tail -5 "${log}" 2>/dev/null)"
fi
info "$(grep 'caught up' "${log}" | tail -1)"

printf '\n\033[32mthe restart converged and caught up the backup\033[0m\n'
