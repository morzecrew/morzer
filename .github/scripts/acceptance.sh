#!/usr/bin/env bash
# The acceptance scenario: the whole lifecycle, against real Docker.
#
# Everything else in this repository tests the manager against fakes, in
# milliseconds. That is the right default and it has one blind spot: nothing
# proves the manager works when Docker is Docker. `just demo` gets close but
# stops before `apply`, because it has no images to run.
#
# So this builds two stub images, pushes them to a throwaway registry, rewrites
# the example bundle to pin the digests that registry returned, and then runs
# the sequence an operator would:
#
#   init → apply → (services stopped, as after a reboot) → apply --startup
#        → status → doctor → backup → restore
#        → (update killed mid-flight, refused, then --resume) → rollback → doctor
#
# Everything the manager touches is real: real Compose, real containers, a real
# health check over a real port, real sops-encrypted secrets. Only the product
# is trivial, because the manager is what is under test.
#
# It runs under `--root`, so it never touches the host's /etc, /var or /run.
#
#   just acceptance          # locally, needs docker and sops
#
set -euo pipefail

ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
MORZER="${MORZER:-${ROOT_DIR}/morzer}"
# Absolute for the same reason WORK is, below: two steps invoke it after a `cd`.
MORZER="$(cd "$(dirname "${MORZER}")" && pwd)/$(basename "${MORZER}")"
REGISTRY="${ACCEPTANCE_REGISTRY:-localhost:5000}"
REGISTRY_NAME="morzer-acceptance-registry"
WORK="${ACCEPTANCE_WORK:-$(mktemp -d -t morzer-acceptance-XXXXXX)}"
mkdir -p "${WORK}"
# Absolute from here on, the same way ROOT_DIR is resolved above.
#
# `mktemp -d` already answers absolutely, so this only matters when a caller
# supplies ACCEPTANCE_WORK -- and it did not matter at all until the support
# bundle steps below, which are the only two commands in this script that run
# from inside ${WORK} rather than from wherever it was invoked. A relative
# ACCEPTANCE_WORK made ${ROOT} and every redirection target resolve a second
# time against the new directory, so the run looked for ${WORK}/${WORK}/root.
WORK="$(cd "${WORK}" && pwd)"
ROOT="${WORK}/root"

# Deliberately not the bundle's default of 18080. The whole point of the
# parameter stages below is that a port the operator chose is the one that gets
# published, checked for conflicts and probed -- and a test that used the
# default would pass whether or not any of that worked.
HTTP_PORT="${ACCEPTANCE_HTTP_PORT:-18099}"

# A second port, so `config set` is proved to *move* a published port rather
# than merely to agree with one already in place.
MOVED_PORT="${ACCEPTANCE_MOVED_PORT:-18098}"

# The air-gapped scenario's port. Its own, for the same reason every other
# scenario here has one: a failure message naming a port has to identify which
# install it came from.
AIRGAP_HTTP_PORT="${ACCEPTANCE_AIRGAP_HTTP_PORT:-18097}"

# The three-tier example's own ports, distinct from the single-tier scenario's
# so the two can never be confused for one another in a failure message.
WEB_HTTP_PORT="${ACCEPTANCE_WEB_HTTP_PORT:-18090}"
WEB_API_PORT="${ACCEPTANCE_WEB_API_PORT:-18091}"
WEB_MOVED_PORT="${ACCEPTANCE_WEB_MOVED_PORT:-18092}"

# Whether this script started the registry, and so owns stopping it.
STARTED_REGISTRY=0

# ----------------------------------------------------------------------------
# Output

step() { printf '\n\033[1m== %s ==\033[0m\n' "$*"; }
info() { printf '   %s\n' "$*"; }
fail() {
	printf '\n\033[31merror: %s\033[0m\n' "$*" >&2
	exit 1
}

# cleanup runs on every exit path, including a failure mid-scenario. A leaked
# Compose project would break the next run with a name conflict, which is a
# confusing way to be told about a failure that happened earlier.
cleanup() {
	local status=$?

	if [ -n "${KEEP_WORK:-}" ]; then
		info "keeping ${WORK}"
	fi

	step "cleaning up"
	docker compose -p demo down --remove-orphans >/dev/null 2>&1 || true
	docker compose -p web down -v --remove-orphans >/dev/null 2>&1 || true
	if [ "${STARTED_REGISTRY}" = "1" ]; then
		docker rm -f "${REGISTRY_NAME}" >/dev/null 2>&1 || true
	fi

	if [ $status -eq 0 ]; then
		printf '\n\033[32macceptance passed\033[0m\n'
	fi
	return $status
}
trap cleanup EXIT

# ----------------------------------------------------------------------------
# Preconditions

require_tool() {
	command -v "$1" >/dev/null 2>&1 || fail "$1 is required and not installed"
}

step "checking prerequisites"
require_tool docker
require_tool sops
docker compose version >/dev/null 2>&1 || fail "docker compose is required"
[ -x "${MORZER}" ] || fail "no morzer binary at ${MORZER}; run \`just build\` first"
info "$(docker --version)"
info "$(sops --version 2>&1 | head -1)"
info "work directory ${WORK}"

# The volume helper image, fetched here rather than left to the backup.
#
# A named volume is read through a container, so `backup` refuses outright when
# the pinned helper is not on the machine -- and a CI runner is precisely that
# machine: it starts with no local images at all. A developer's laptop has
# usually pulled busybox for something else, which is why this scenario passed
# by hand and failed on every push to main.
#
# Which image that is has to be decided the way the manager decides it, or this
# fetches one thing and `backup` waits for another.
#
# MORZER_VOLUME_HELPER_IMAGE wins when set, because it is the escape hatch for
# an operator whose registry does not carry busybox -- an air-gapped mirror that
# sets it would otherwise be failed here for not having a default it does not
# want.
#
# Trimmed first, because WithHelperImage trims it and stores it trimmed: it
# arrives from an environment variable, and a systemd `Environment=` line with a
# trailing space is the ordinary way one picks up whitespace. Untrimmed, this
# would inspect and pull one reference while the manager ran another, and a
# value that is nothing but spaces would count as an override here and as unset
# there -- both of them the same class of bug this block exists to close.
#
# Trimmed in the shell rather than through sed, which is line-oriented: it
# strips per line, so a value led by a newline comes back still carrying it
# while Go's TrimSpace takes it off. The two expansions below cut the leading
# and trailing whitespace runs from the value as a whole, newlines included.
#
# Empty after trimming counts as unset, which is what HelperImage does with an
# empty override. Then the digest comes out of the manager's own source, because
# the manager accepts no other; hardcoding it would drift the day it is bumped.
HELPER_IMAGE="${MORZER_VOLUME_HELPER_IMAGE:-}"
HELPER_IMAGE="${HELPER_IMAGE#"${HELPER_IMAGE%%[![:space:]]*}"}"
HELPER_IMAGE="${HELPER_IMAGE%"${HELPER_IMAGE##*[![:space:]]}"}"
if [ -n "${HELPER_IMAGE}" ]; then
	info "volume helper overridden by MORZER_VOLUME_HELPER_IMAGE"
else
	HELPER_IMAGE_SRC="${ROOT_DIR}/internal/adapters/runtime/compose/volumes.go"
	HELPER_IMAGE="$(sed -n 's/^const DefaultHelperImage = "\(.*\)"$/\1/p' "${HELPER_IMAGE_SRC}")"
	[ -n "${HELPER_IMAGE}" ] ||
		fail "cannot read DefaultHelperImage from ${HELPER_IMAGE_SRC}"
fi

if docker image inspect "${HELPER_IMAGE}" >/dev/null 2>&1; then
	info "volume helper ${HELPER_IMAGE} (already local)"
else
	info "pulling volume helper ${HELPER_IMAGE}"
	docker pull "${HELPER_IMAGE}" >/dev/null ||
		fail "cannot pull the volume helper ${HELPER_IMAGE}; volumes are read through it, so backup cannot run without it"
fi

# ----------------------------------------------------------------------------
# A registry, so images can be pinned by digest
#
# An image built locally has no digest until it is pushed -- `RepoDigests` is
# empty -- and the manifest requires `name@sha256:…`. There is no way around
# that short of weakening the rule that makes a release immutable, so the
# scenario runs a registry instead. `localhost` is the one host Docker treats as
# insecure by default, which is why it is not named by IP.

start_registry() {
	step "starting a throwaway registry at ${REGISTRY}"

	if curl -fsS "http://${REGISTRY}/v2/" >/dev/null 2>&1; then
		info "one is already listening; using it"
		return
	fi

	docker rm -f "${REGISTRY_NAME}" >/dev/null 2>&1 || true
	docker run -d --rm --name "${REGISTRY_NAME}" \
		-p "${REGISTRY##*:}:5000" registry:2 >/dev/null
	STARTED_REGISTRY=1

	for _ in $(seq 1 30); do
		if curl -fsS "http://${REGISTRY}/v2/" >/dev/null 2>&1; then
			info "ready"
			return
		fi
		sleep 1
	done
	fail "the registry did not become ready"
}

# push_stub builds one stub image and prints the digest the registry assigned.
#
# The repository defaults to demo/ so the existing calls read unchanged; the
# three-tier example passes web/.
push_stub() {
	local name=$1 repo="${2:-demo}" tag
	tag="${REGISTRY}/${repo}/${name}:acceptance"

	docker build --quiet -t "${tag}" "${ROOT_DIR}/testdata/acceptance/${name}" >/dev/null
	docker push --quiet "${tag}" >/dev/null

	# The digest the registry computed for *this* repository, which is what
	# the manifest pins.
	#
	# Selected by prefix rather than by index: two repositories can hold the
	# same image -- the three-tier example reuses the db stub -- and then
	# RepoDigests holds an entry for each. Taking element 0 pinned web/db to
	# the digest demo/db had been given, and the manifest was rewritten with
	# a reference containing two @ signs.
	docker inspect --format '{{range .RepoDigests}}{{println .}}{{end}}' "${tag}" |
		grep "^${REGISTRY}/${repo}/${name}@" |
		head -1 |
		sed "s|^${REGISTRY}/${repo}/${name}@||"
}

# ----------------------------------------------------------------------------
# The bundles
#
# Derived from the fixtures the rest of the suite runs against, rather than
# maintained separately: an acceptance bundle that drifted from the one the unit
# tests use would be an acceptance run proving something about a bundle nobody
# ships.

prepare_bundle() {
	local src=$1 dest=$2

	cp -r "${ROOT_DIR}/${src}" "${dest}"

	# Only the two image lines change. They are the one thing that cannot be
	# checked in, because a digest does not exist until a registry issues it.
	sed -i \
		-e "s|^  app: .*|  app: ${REGISTRY}/demo/app@${APP_DIGEST}|" \
		-e "s|^  db: .*|  db: ${REGISTRY}/demo/db@${DB_DIGEST}|" \
		"${dest}/manifest.yaml"

	grep -q "@sha256:" "${dest}/manifest.yaml" ||
		fail "the manifest in ${dest} was not rewritten with real digests"
}

# prepare_web_bundle does the same for the three-tier example, which pins three.
prepare_web_bundle() {
	local dest=$1

	cp -r "${ROOT_DIR}/testdata/bundle-web" "${dest}"
	sed -i \
		-e "s|^  frontend: .*|  frontend: ${REGISTRY}/web/frontend@${FRONTEND_DIGEST}|" \
		-e "s|^  backend: .*|  backend: ${REGISTRY}/web/backend@${BACKEND_DIGEST}|" \
		-e "s|^  db: .*|  db: ${REGISTRY}/web/db@${WEB_DB_DIGEST}|" \
		"${dest}/manifest.yaml"

	grep -c "@sha256:" "${dest}/manifest.yaml" | grep -qx 3 ||
		fail "the three-tier manifest was not rewritten with three real digests"
}

# web_port reads a published host port out of the three-tier project.
web_port() {
	docker compose -p web port "$1" 8080 2>/dev/null | sed 's/.*://'
}

# ----------------------------------------------------------------------------
# Assertions

# expect_exit runs a command and asserts its exit status. The exit codes are a
# published contract, so the scenario checks them rather than only checking that
# something failed.
expect_exit() {
	local want=$1
	shift
	local got=0
	"$@" || got=$?
	[ "${got}" = "${want}" ] ||
		fail "expected exit ${want} from '$*', got ${got}"
}

# status_field reads one field out of `status --json`, which is a published
# contract and so is what the scenario asserts against rather than parsing
# human output.
status_field() {
	"${MORZER}" --root "${ROOT}" --json status | jq -r "$1"
}

# Counting is not instantaneous after a stop. `docker compose stop` returns
# when it has asked; Docker keeps reporting a container as running until its
# process is actually gone, so a count sampled the moment the command returns
# can still see a container the operation has already finished with.
#
# Polling to the expected count rather than sampling once does not weaken the
# assertion: a wrong count still fails, it merely has to still be wrong a few
# seconds later. What it removes is the failure that is a fact about timing
# rather than about the deployment.
assert_running() {
	local expected=$1
	local running deadline
	deadline=$(( $(date +%s) + 30 ))
	while :; do
		running=$(docker compose -p demo ps --status running --format '{{.Name}}' | wc -l)
		if [ "${running}" = "${expected}" ]; then
			break
		fi
		if [ "$(date +%s)" -ge "${deadline}" ]; then
			fail "expected ${expected} running container(s), found ${running} after 30s"
		fi
		sleep 1
	done
	info "${running} container(s) running"
}

# ----------------------------------------------------------------------------
# Scenario

start_registry

step "building and pushing the stub images"
APP_DIGEST=$(push_stub app)
DB_DIGEST=$(push_stub db)
info "app ${APP_DIGEST}"
info "db  ${DB_DIGEST}"

step "preparing the bundles"
prepare_bundle testdata/bundle "${WORK}/bundle-1.2.0"
prepare_bundle testdata/bundle-1.3.0 "${WORK}/bundle-1.3.0"
info "1.2.0 and 1.3.0, pinned to the pushed digests"

step "release verify"
"${MORZER}" release verify "${WORK}/bundle-1.2.0"

step "init"
mkdir -p "${WORK}/keys"
RECOVERY=$("${MORZER}" secret recipients generate-recovery-key "${WORK}/keys/recovery.key" | tail -1)
info "recovery recipient ${RECOVERY}"

"${MORZER}" --root "${ROOT}" init \
	--release "${WORK}/bundle-1.2.0" \
	--profile embedded \
	--domain acceptance.example \
	--recovery-recipient "${RECOVERY}" \
	--install-units=false \
	--set http_port="${HTTP_PORT}" \
	--set log_level=debug

step "an undeclared parameter is refused"
if "${MORZER}" --root "${WORK}/reject" --plain init \
	--release "${WORK}/bundle-1.2.0" --no-recovery-recipient \
	--install-units=false --set htpp_port=9000 >/dev/null 2>&1; then
	fail "a parameter the release does not declare was accepted"
fi
info "a typo in --set is refused"

step "apply"
"${MORZER}" --root "${ROOT}" apply
assert_running 2

# The reason parameters exist. Compose publishes the port, preflight checks the
# port, and the manager's health probe asks the port -- all from one value. Get
# any of the three wrong and apply above would already have failed at "wait for
# health checks", but assert the published port directly so the failure names
# the cause rather than the symptom.
step "the deployment is published on the port that was set"
published=$(docker compose -p demo port app 18080 2>/dev/null | sed 's/.*://')
[ "${published}" = "${HTTP_PORT}" ] ||
	fail "published on ${published:-nothing}, expected ${HTTP_PORT}"
curl -fsS "http://127.0.0.1:${HTTP_PORT}/health/ready" >/dev/null ||
	fail "nothing answers on the port the parameter set"
info "app is published and answering on ${HTTP_PORT}"

step "an operator value reaches the container, a default fills the rest"
docker compose -p demo exec -T app printenv DEMO_LOG_LEVEL 2>/dev/null | grep -qx debug ||
	fail "the log_level parameter did not reach the container"
info "DEMO_LOG_LEVEL=debug inside the container"

step "config set moves the published port on a running deployment"
# The trap this stage exists for: `docker compose restart` restarts the
# *existing* containers, and a published port is fixed when a container is
# created. Restarting after a port change reports success and leaves the old
# mapping in place. Only re-creating works, and only a real Docker run can tell
# the difference.
"${MORZER}" --root "${ROOT}" config set http_port="${MOVED_PORT}"
published=$(docker compose -p demo port app 18080 2>/dev/null | sed 's/.*://')
[ "${published}" = "${MOVED_PORT}" ] ||
	fail "config set left the port at ${published:-nothing}, expected ${MOVED_PORT}"
curl -fsS "http://127.0.0.1:${MOVED_PORT}/health/ready" >/dev/null ||
	fail "nothing answers on the port config set moved it to"
info "config set moved the published port to ${MOVED_PORT}"

step "config get reports it, and doctor still passes"
[ "$("${MORZER}" --root "${ROOT}" config get http_port)" = "${MOVED_PORT}" ] ||
	fail "config get does not report the value config set recorded"
"${MORZER}" --root "${ROOT}" doctor >/dev/null || fail "doctor fails after config set"

step "config unset returns it to the release default"
"${MORZER}" --root "${ROOT}" config unset http_port
[ "$("${MORZER}" --root "${ROOT}" config get http_port)" = "18080" ] ||
	fail "config unset did not restore the release default"
published=$(docker compose -p demo port app 18080 2>/dev/null | sed 's/.*://')
[ "${published}" = "18080" ] || fail "unset left the port at ${published:-nothing}"
info "config unset restored the release default and re-created the service"

step "a hand edit to installation.yaml is reported, not obeyed"
sed -i 's/^profile: embedded/profile: external-db/' "${ROOT}/etc/demo/installation.yaml"
"${MORZER}" --root "${ROOT}" doctor 2>&1 | grep -q 'installation.yaml' ||
	fail "doctor does not report a hand edit that changes nothing"
"${MORZER}" --root "${ROOT}" config set log_level=warn >/dev/null
grep -q '^profile: embedded' "${ROOT}/etc/demo/installation.yaml" ||
	fail "config set did not rewrite installation.yaml from the recorded state"
info "the drift is reported and the next config set corrects the file"


# Installation settings, which are not release parameters
#
# RFC 0016 P1 shipped `update.check` with a documented name, a default, a doctor
# check and no way at all to turn it on: its own refusal pointed at `morzer
# config`, which read and wrote release parameters exclusively. This proves the
# surface that closed it, on a real machine, against the real state file.
step "installation settings are settable and are not release parameters"
"${MORZER}" --root "${ROOT}" config settings | grep -q 'update.check' ||
	fail "config settings does not list update.check"

"${MORZER}" --root "${ROOT}" config set update.check=true >/dev/null ||
	fail "update.check cannot be turned on"
[ "$("${MORZER}" --root "${ROOT}" config get update.check)" = "true" ] ||
	fail "update.check did not persist"
grep -q 'check: true' "${ROOT}/etc/demo/installation.yaml" ||
	fail "the setting did not reach installation.yaml"

# A setting and a parameter run on different machinery -- one writes a flag, the
# other re-creates containers -- so a mixed command is refused rather than
# half-applied.
if "${MORZER}" --root "${ROOT}" config set update.check=false log_level=warn >/dev/null 2>&1; then
	fail "a setting and a parameter were set in one command"
fi
"${MORZER}" --root "${ROOT}" config unset update.check >/dev/null ||
	fail "update.check cannot be turned off again"
info "update.check toggles, and mixing a setting with a parameter is refused"


step "the rendered configuration holds paths, never values"
config="${ROOT}/etc/demo/application.yaml"
[ -f "${config}" ] || fail "no rendered configuration at ${config}"
secret=$(cat "${ROOT}/run/demo/secrets/db_password")
if grep -qF "${secret}" "${config}"; then
	fail "the rendered configuration contains a secret value"
fi
info "no secret value in ${config}"

step "apply is idempotent"
"${MORZER}" --root "${ROOT}" apply
assert_running 2

step "status and doctor"
"${MORZER}" --root "${ROOT}" status
"${MORZER}" --root "${ROOT}" doctor

step "into the running deployment: ps, logs, stats, exec"
# The four read-only commands, against the deployment that is up. What this
# proves and no fake can: that the flags are flags Docker accepts, that the log
# framing the manager parses is the framing Compose produces, and that a
# container's exit code survives all the way out to $?.
"${MORZER}" --root "${ROOT}" ps

# Every container the project runs, named. `ps` keyed on the service alone
# could not tell two replicas apart.
"${MORZER}" --root "${ROOT}" --json ps |
	jq -e '.data | length == 2 and all(.[]; .container != "")' >/dev/null ||
	fail "ps did not report both containers with their instances"

# The health check has been polling the app stub, which logs each request, so
# there are real lines to read -- eventually.
#
# Waited for rather than sampled once, for the same reason assert_running is:
# whether a line exists yet depends on the health check having polled, which
# nothing here synchronises with. Sampled immediately this asserts how promptly
# a container flushed its first line, which is a fact about the machine. The
# claim under test is that the manager frames what Compose produces, and that
# is only testable once there is something to frame.
wait_for_log_line() {
	local deadline
	deadline=$(( $(date +%s) + 60 ))
	while :; do
		if "${MORZER}" --root "${ROOT}" logs --tail 20 | grep -q "|"; then
			return 0
		fi
		if [ "$(date +%s)" -ge "${deadline}" ]; then
			fail "logs produced no framed line within 60s"
		fi
		sleep 1
	done
}
wait_for_log_line

# The one exception to the single-envelope contract: one JSON object per line,
# and no envelope at the end. A consumer parsing lines must never meet one.
LOG_JSON=$("${MORZER}" --root "${ROOT}" --json logs --tail 20)
[ -n "${LOG_JSON}" ] || fail "logs --json produced nothing"
printf '%s\n' "${LOG_JSON}" | jq -e -s 'all(.[]; has("line") and (has("ok") | not))' >/dev/null ||
	fail "logs --json did not emit exactly one record per line"
printf '%s\n' "${LOG_JSON}" | jq -e -s 'any(.[]; .service != "" and .ts != null)' >/dev/null ||
	fail "no log record carried the service and the instant its container wrote it"

# A running container uses memory. Zero would mean the adapter read the
# daemon's answer wrongly rather than that the service is frugal.
"${MORZER}" --root "${ROOT}" --json stats |
	jq -e '.data | length >= 1 and all(.[]; .memory_bytes > 0)' >/dev/null ||
	fail "stats reported no memory for a running container"

# The command's own stdout, unframed: an operator piping this into a file must
# get what the command printed and not a report about it.
EXEC_OUT=$("${MORZER}" --root "${ROOT}" exec app -- printf 'hello-from-the-container')
[ "${EXEC_OUT}" = "hello-from-the-container" ] ||
	fail "exec did not pass the command's output through: '${EXEC_OUT}'"

# And its exit code. A manager that returned 0 here would make `morzer exec`
# unusable in a script, which is most of what it is for.
expect_exit 42 "${MORZER}" --root "${ROOT}" exec app -- sh -c 'exit 42'

# The refusal, against a service that is really stopped.
docker compose -p demo stop db >/dev/null
expect_exit 7 "${MORZER}" --root "${ROOT}" exec db -- true
docker compose -p demo start db >/dev/null

# Journalled with the argv, and never with the output. The journal is where a
# later reader learns that a human was inside the deployment at 03:14 and what
# they asked it to do.
#
# Asserted on the record's *shape* rather than by grepping for the output: the
# argv legitimately contains whatever the operator typed, including the string
# the command was told to print, so a grep would fail on a correct journal. The
# three flags are the whole record, so anything else -- an output field somebody
# added for convenience -- fails here. The text assertion belongs where argv and
# output can be arranged to differ, which is the fake-backed suite.
grep '"type":"exec"' "${ROOT}/var/lib/demo/manager/operations.jsonl" |
	jq -e -s 'length >= 2 and all(.[]; .flags | keys == ["argv", "exit_code", "service"])' >/dev/null ||
	fail "the exec journal records are not the three flags and nothing else"

# And the argv is redacted before it is written, which is what a password in a
# connection string depends on. The value is this installation's real generated
# secret, read off the tmpfs the manager rendered it to.
db_password=$(cat "${ROOT}/run/demo/secrets/db_password")
[ -n "${db_password}" ] || fail "no rendered db_password to test the redaction with"
"${MORZER}" --root "${ROOT}" exec app -- echo "postgres://demo:${db_password}@db/demo" >/dev/null
if grep -qF "${db_password}" "${ROOT}/var/lib/demo/manager/operations.jsonl"; then
	fail "a known secret value reached the journal in an argv"
fi

step "a reboot: services stopped, then apply --startup"
# Restarting the Docker daemon is what a reboot does to a deployment, but doing
# it on a shared runner would disrupt everything else. Stopping the project's
# containers reproduces the state the boot-time path has to recover from.
docker compose -p demo stop >/dev/null
assert_running 0
"${MORZER}" --root "${ROOT}" apply --startup
assert_running 2

step "a backup target off this machine"
# file:// is the target that needs no credential, and the one a recovery can
# always reach. Configured before the backup so the push is part of the same
# operation an operator would run.
#
# A directory under the test root, not a second device. What is under test is
# that the manager pushes, lists, verifies and prunes there -- whether the
# operator picked storage that survives the machine is their decision, and
# arranging a real second device would need privileges CI does not have.
offsite="${ROOT}/offsite"
"${MORZER}" --root "${ROOT}" backup target add "file://${offsite}"
"${MORZER}" --root "${ROOT}" backup target list

step "backup"
echo "acceptance-marker" > "${ROOT}/var/lib/demo/data/marker"
"${MORZER}" --root "${ROOT}" backup --reason acceptance
"${MORZER}" --root "${ROOT}" backup list

step "the backup left the machine"
remote_id=$("${MORZER}" --root "${ROOT}" --json backup list --remote | jq -r '.data[0].manifest.id')
[ -n "${remote_id}" ] && [ "${remote_id}" != "null" ] ||
	fail "the backup was reported as taken but is not on the target"
info "${remote_id} is on ${offsite}"

# Every component on the target is ciphertext apart from the manifest. This is
# what makes a target safe to use at all, and it is checked here against the
# bytes a real backup hook produced rather than against a fixture.
if grep -rl "acceptance-marker" "${offsite}" >/dev/null 2>&1; then
	fail "a component on the target carries readable plaintext"
fi
info "everything on the target except its manifest is encrypted"

step "the copy on the target verifies without a key"
"${MORZER}" --root "${ROOT}" backup verify --remote

step "doctor is satisfied that the backup arrived"
"${MORZER}" --root "${ROOT}" --json doctor |
	jq -e '.data.results[] | select(.id == "backup.target-freshness") | .status == "ok"' >/dev/null ||
	fail "doctor does not agree that the most recent backup reached a target"

step "restore"
installation=$(status_field '.data.installation_id')
info "installation ${installation}"
rm -f "${ROOT}/var/lib/demo/data/marker"
"${MORZER}" --root "${ROOT}" restore --force --confirm "${installation}"
[ "$(cat "${ROOT}/var/lib/demo/data/marker")" = "acceptance-marker" ] ||
	fail "the restore did not bring the data back"
info "data restored"

step "restore refuses without the typed confirmation"
expect_exit 2 "${MORZER}" --root "${ROOT}" restore --force --confirm wrong-id

# ----------------------------------------------------------------------------
# A crash mid-update: SIGKILL, the artifact every other test tier only
# simulates. The kill waits for the journal to show the pre-update backup
# completed -- the one step that is not safe to repeat -- and lands in the
# staging/convergence phase behind it, where every step is idempotent and the
# process has seconds of real Docker work ahead. What is asserted afterwards
# is the whole crash-recovery contract: the wreck is visible in the journal, a
# plain update refuses to drive over it, and --resume carries the completed
# backup's credit forward and rescues the machine to the version the operator
# asked for.
step "an update is killed mid-flight, after its pre-update backup"
journal="${ROOT}/var/lib/demo/manager/operations.jsonl"
"${MORZER}" --root "${ROOT}" update "${WORK}/bundle-1.3.0" >/dev/null 2>&1 &
update_pid=$!
for _ in $(seq 1 400); do
	grep -q '"id":"pre-update-backup","status":"succeeded"' "${journal}" 2>/dev/null && break
	sleep 0.05
done
grep -q '"id":"pre-update-backup","status":"succeeded"' "${journal}" ||
	fail "the update never got past its pre-update backup, so the kill had nowhere safe to land"
kill -9 "${update_pid}" 2>/dev/null || true
wait "${update_pid}" 2>/dev/null || true
tail -1 "${journal}" | jq -e '.type == "update" and .status == "running"' >/dev/null ||
	fail "the killed update did not leave a running record in the journal"
info "killed mid-update; the journal holds the wreck"

step "a plain update refuses to drive over the wreck"
if "${MORZER}" --root "${ROOT}" update "${WORK}/bundle-1.3.0" >/dev/null 2>&1; then
	fail "an update ran straight over an operation the crash left unfinished"
fi
refused=$("${MORZER}" --root "${ROOT}" --json update "${WORK}/bundle-1.3.0" 2>/dev/null || true)
echo "${refused}" | jq -e '((.error.message // "") + " " + (.error.hint // "")) | test("did not finish|resume")' >/dev/null ||
	fail "the refusal must name the unfinished operation and the way forward"
info "refused, naming the way forward"

step "update --resume rescues the machine to 1.3.0"
"${MORZER}" --root "${ROOT}" update --resume "${WORK}/bundle-1.3.0"
assert_running 2
version=$(status_field '.data.current_release.version')
[ "${version}" = "1.3.0" ] || fail "expected 1.3.0 after the resumed update, got ${version}"
info "running ${version}"

# The most valuable assertion in the scenario, and the one it took a real
# migration to make: 1.3.0's migrate hook moved the database schema to 14, and
# 1.2.0 declares it can read at most 12. Swapping the containers back would put
# an old binary in front of a schema it cannot understand, which corrupts data
# quietly rather than failing loudly. So the rollback must be refused.
step "rollback is refused: the schema has moved past what 1.2.0 can read"
expect_exit 9 "${MORZER}" --root "${ROOT}" rollback
assert_running 2
version=$(status_field '.data.current_release.version')
[ "${version}" = "1.3.0" ] || fail "a refused rollback must change nothing, got ${version}"
info "still running ${version}, as a refusal should leave it"

# --force authorises destructive actions, not incorrect ones. If this ever
# starts succeeding, the guarantee the refusal exists for is gone.
step "--force does not override the refusal"
expect_exit 9 "${MORZER}" --root "${ROOT}" rollback --force
version=$(status_field '.data.current_release.version')
[ "${version}" = "1.3.0" ] || fail "--force overrode a safety refusal, got ${version}"
info "still ${version}"

step "the refusal names the backup to restore from instead"
# Captured rather than piped: the command exits 9 on purpose, and `pipefail`
# would read that as the pipeline failing however well jq did.
refusal=$("${MORZER}" --root "${ROOT}" --json rollback 2>/dev/null || true)
echo "${refusal}" | jq -e '.error.hint | test("restore --backup")' >/dev/null ||
	fail "the refusal must name the remedy, not only the problem"
echo "${refusal}" | jq -e '.exit_code == 9' >/dev/null ||
	fail "a refused rollback must report the incompatible exit code"
info "$(echo "${refusal}" | jq -r '.error.hint')"

step "doctor, after everything"
"${MORZER}" --root "${ROOT}" doctor

# ----------------------------------------------------------------------------
# The support bundle, on the one installation that has actually lived
#
# Here rather than earlier because everything above it is what makes the
# measurement mean anything: this installation has run init, apply, three
# configuration changes, a backup, a restore, an update killed mid-flight, a
# resume and a refused rollback. RFC 0024 §11.3 owed the log bound a number from
# a populated deployment rather than a guess, and a bundle taken from a fresh
# install would have measured an empty journal.

step "support bundle --preview writes nothing and says what would leave"
"${MORZER}" --root "${ROOT}" --json support bundle --preview >"${WORK}/support-preview.json" ||
	fail "support bundle --preview failed: $(cat "${WORK}/support-preview.json")"
jq -e '.data.preview == true and (.data.path // "") == ""' \
	"${WORK}/support-preview.json" >/dev/null ||
	fail "--preview reported a path: $(jq -c '.data | {preview, path}' "${WORK}/support-preview.json")"
# The refusals are enforced in Go against the archive's bytes; what this checks
# is the operator-facing half -- that the preview names the components rather
# than reporting a total somebody has to trust.
for component in journal.jsonl doctor.json manifest.yaml meta.json; do
	jq -e --arg c "${component}" '[.data.entries[] | select(.name == $c)] | length == 1' \
		"${WORK}/support-preview.json" >/dev/null ||
		fail "the preview does not name ${component}"
done
info "$(jq -r '.data.entries | length' "${WORK}/support-preview.json") component(s) named, nothing written"

step "support bundle produces an archive a stranger can open"
(cd "${WORK}" && "${MORZER}" --root "${ROOT}" --json support bundle >"${WORK}/support.json") ||
	fail "support bundle failed: $(cat "${WORK}/support.json")"
BUNDLE=$(jq -r '.data.path' "${WORK}/support.json")
[ -f "${BUNDLE}" ] || fail "support bundle reported ${BUNDLE}, which does not exist"

# With `tar`, not with the manager: the recipient of this file has neither
# morzer nor a reason to install it.
tar --zstd -tf "${BUNDLE}" >"${WORK}/support-entries.txt" 2>/dev/null ||
	fail "the archive does not open with tar --zstd"
grep -qx 'meta.json' "${WORK}/support-entries.txt" ||
	fail "the archive has no meta.json: $(cat "${WORK}/support-entries.txt")"

# The measurement RFC 0024 §11.3 asked for, printed rather than asserted: it is
# a fact about a deployment, and a threshold here would fail the build the first
# time somebody added a step to this script.
# And once in the form an operator actually sees, which is the output the
# documentation quotes. A sample nobody captured from a running binary is a
# sample that drifts the first time a column moves.
step "the same run, as an operator sees it"
(cd "${WORK}" && "${MORZER}" --root "${ROOT}" support bundle) ||
	fail "support bundle failed in plain mode"

BUNDLE_BYTES=$(stat -c%s "${BUNDLE}")
JOURNAL_BYTES=$(jq -r '[.data.entries[] | select(.name == "journal.jsonl") | .bytes] | first // 0' "${WORK}/support.json")
LOG_BYTES=$(jq -r '[.data.entries[] | select(.name | startswith("logs/")) | .bytes] | add // 0' "${WORK}/support.json")
info "archive ${BUNDLE_BYTES} B compressed; journal ${JOURNAL_BYTES} B; logs ${LOG_BYTES} B uncompressed"

# ----------------------------------------------------------------------------
# The fleet row, on the same installation
#
# Here for the same reason the support bundle is: the row reports health, drift
# and the last operation, and every one of those is a placeholder on an
# installation that has only ever run `init`. This one has lived.
#
# A directory target rather than a bucket. The transports are held to the object
# store contract in Go against all three; what this scenario proves is the part
# no unit test can -- that the real binary, wired the way `main` wires it,
# writes a row somebody else's copy of the binary reads back.

FLEET_TARGET="${WORK}/fleet-target"
mkdir -p "${FLEET_TARGET}"

step "a target to publish the row to"
# Added as a backup target, because that is what the design reuses: RFC 0026
# decision 3 keeps one registry and one list to configure. Every publish below
# names it explicitly with --target, so this scenario asserts about one target
# rather than about however many earlier steps happen to have added.
"${MORZER}" --root "${ROOT}" backup target add "file://${FLEET_TARGET}" ||
	fail "cannot add the fleet target"

step "fleet publish --dry-run shows the row and writes nothing"
"${MORZER}" --root "${ROOT}" --json fleet publish --dry-run --target "file://${FLEET_TARGET}" >"${WORK}/fleet-dry.json" ||
	fail "fleet publish --dry-run failed: $(cat "${WORK}/fleet-dry.json")"
jq -e '.data.row.schema == 1 and .data.row.bound != ""' "${WORK}/fleet-dry.json" >/dev/null ||
	fail "--dry-run described the row instead of producing it"
[ -z "$(find "${FLEET_TARGET}" -name status.json -print -quit)" ] ||
	fail "fleet publish --dry-run wrote a row"

step "fleet publish writes the row and its signature"
"${MORZER}" --root "${ROOT}" --json fleet publish --target "file://${FLEET_TARGET}" >"${WORK}/fleet-publish.json" ||
	fail "fleet publish failed: $(cat "${WORK}/fleet-publish.json")"
FLEET_KEY=$(jq -r '.data.key' "${WORK}/fleet-publish.json")
[ -f "${FLEET_TARGET}/${FLEET_KEY}" ] ||
	fail "fleet publish reported ${FLEET_KEY}, which is not on the target"
[ -f "${FLEET_TARGET}/${FLEET_KEY}.minisig" ] ||
	fail "the row was published without a signature beside it"

# The one check no Go test can make, because it is about a tool this project
# does not own: what the manager writes is what `minisign` verifies. The whole
# reason the signature is detached and over the bytes as published is that
# `minisign -Vm` works on the document unmodified.
if command -v minisign >/dev/null 2>&1; then
	FLEET_KEYLINE=$(jq -r '.data.row.signing_key' "${WORK}/fleet-publish.json")
	minisign -Vm "${FLEET_TARGET}/${FLEET_KEY}" -P "${FLEET_KEYLINE}" >/dev/null ||
		fail "minisign will not verify the row this manager signed"
	info "minisign verifies the published row"
else
	info "minisign is not installed; the row's signature was not checked with it"
fi

step "the row carries no parameter value and no secret"
# The refusals are enforced in Go against the published bytes. What this adds is
# the real installation's real values: the parameters this scenario actually set
# are the ones a leak would carry, and no fixture can stand in for them.
"${MORZER}" --root "${ROOT}" --json config list >"${WORK}/fleet-params.json" ||
	fail "config list failed: $(cat "${WORK}/fleet-params.json")"
jq -r '[.. | objects | .value? // empty | select(type == "string" and length > 2)] | .[]' \
	"${WORK}/fleet-params.json" | sort -u >"${WORK}/fleet-forbidden.txt"
[ -s "${WORK}/fleet-forbidden.txt" ] ||
	fail "no parameter values were found, so this check proves nothing"
while IFS= read -r forbidden; do
	grep -qF -- "${forbidden}" "${FLEET_TARGET}/${FLEET_KEY}" &&
		fail "the parameter value ${forbidden} reached the published row"
done <"${WORK}/fleet-forbidden.txt"
grep -qF 'demo.example' "${FLEET_TARGET}/${FLEET_KEY}" &&
	fail "a hostname reached the published row"
info "$(wc -l <"${WORK}/fleet-forbidden.txt") parameter value(s) checked against the row"

step "publishing again declines to replace a newer row"
# The read-before-write, against a real target. The row just published is
# stamped now, and `--dry-run` is not involved: this is a second real publish
# whose clock cannot be ahead of the first.
"${MORZER}" --root "${ROOT}" --json fleet publish --target "file://${FLEET_TARGET}" >"${WORK}/fleet-again.json" ||
	fail "the second fleet publish failed: $(cat "${WORK}/fleet-again.json")"
jq -e '[.data.targets[] | select(.published or .declined != null)] | length == 1' \
	"${WORK}/fleet-again.json" >/dev/null ||
	fail "the second publish neither published nor declined: $(jq -c '.data.targets' "${WORK}/fleet-again.json")"

step "fleet ls reads the row back"
"${MORZER}" --root "${ROOT}" --json fleet ls "file://${FLEET_TARGET}" >"${WORK}/fleet-ls.json" ||
	fail "fleet ls failed: $(cat "${WORK}/fleet-ls.json")"
jq -e '.data.rows | length == 1' "${WORK}/fleet-ls.json" >/dev/null ||
	fail "fleet ls found $(jq -r '.data.rows | length' "${WORK}/fleet-ls.json") row(s), expected 1"
jq -e '.data.rows[0].problem == null and .data.rows[0].signature == "signed"' \
	"${WORK}/fleet-ls.json" >/dev/null ||
	fail "the row read back carries a problem: $(jq -c '.data.rows[0]' "${WORK}/fleet-ls.json")"

# The refusal this phase lives or dies by (RFC 0026 §8): a reader with no roster
# must never present a row as verified, and must say so rather than leaving it
# to the documentation.
jq -e '.data.rows[0].signature != "verified"' "${WORK}/fleet-ls.json" >/dev/null ||
	fail "fleet ls claimed a row was verified with no roster to anchor it"
jq -e '(.data.limitations | length) > 0 and (.data.limitations | join(" ") | contains("roster"))' \
	"${WORK}/fleet-ls.json" >/dev/null ||
	fail "fleet ls printed a table without saying what it could not see"

step "a row nobody can read is a row, not an omission"
# Written by hand at a key this manager would never have produced, which is what
# a bucket several machines write to eventually contains.
mkdir -p "${FLEET_TARGET}/fleet/impostor/inst_ACCEPTANCE"
printf 'this is not JSON at all\n' >"${FLEET_TARGET}/fleet/impostor/inst_ACCEPTANCE/status.json"
"${MORZER}" --root "${ROOT}" --json fleet ls "file://${FLEET_TARGET}" >"${WORK}/fleet-bad.json" && {
	fail "fleet ls exited zero with an unreadable row on the target"
}
jq -e '[.data.rows[] | select(.problem != null)] | length == 1' "${WORK}/fleet-bad.json" >/dev/null ||
	fail "the unreadable row was dropped instead of shown: $(jq -c '.data.rows' "${WORK}/fleet-bad.json")"
rm -rf "${FLEET_TARGET:?}/fleet/impostor"

# And once in the form an operator actually sees, which is the output the
# documentation quotes. A sample nobody captured from a running binary is a
# sample that drifts the first time a column moves.
step "the same listing, as an operator sees it"
"${MORZER}" --root "${ROOT}" fleet ls "file://${FLEET_TARGET}" ||
	fail "fleet ls failed in plain mode"

FLEET_BYTES=$(stat -c%s "${FLEET_TARGET}/${FLEET_KEY}")
info "the published row is ${FLEET_BYTES} B"

# ----------------------------------------------------------------------------
# The three-tier example
#
# A separate, shorter scenario: the lifecycle is already proven above, and what
# this bundle exists to demonstrate is what one tier cannot show -- two tiers
# each publishing their own port from their own parameter, credentials scoped to
# the tier that needs them, and a change to one tier leaving the others running.
#
# It is the bundle the documentation site's second worked example is drawn from,
# so it has to be a bundle that runs.

step "the three-tier example: building its images"
FRONTEND_DIGEST=$(push_stub frontend web)
BACKEND_DIGEST=$(push_stub backend web)
WEB_DB_DIGEST=$(push_stub db web)
prepare_web_bundle "${WORK}/bundle-web"
"${MORZER}" release verify "${WORK}/bundle-web"

step "the three-tier example: install and converge"
WEB_ROOT="${WORK}/web-root"
"${MORZER}" --root "${WEB_ROOT}" init \
	--release "${WORK}/bundle-web" \
	--profile embedded \
	--domain web.example \
	--no-recovery-recipient \
	--install-units=false \
	--set http_port="${WEB_HTTP_PORT}" \
	--set api_port="${WEB_API_PORT}"
"${MORZER}" --root "${WEB_ROOT}" apply

step "each tier answers on the port its own parameter set"
# Asserting the *body* as well as the status: two stubs both answering "ok"
# would let a swapped port mapping pass, which is precisely the failure a
# multi-tier example is supposed to catch.
[ "$(curl -fsS "http://127.0.0.1:${WEB_HTTP_PORT}/health/ready")" = "frontend" ] ||
	fail "port ${WEB_HTTP_PORT} did not reach the frontend"
[ "$(curl -fsS "http://127.0.0.1:${WEB_API_PORT}/health/ready")" = "backend" ] ||
	fail "port ${WEB_API_PORT} did not reach the backend"
info "frontend on ${WEB_HTTP_PORT}, backend on ${WEB_API_PORT}"

step "only the backend holds the database credential"
docker compose -p web exec -T backend cat /run/secrets/db_password >/dev/null 2>&1 ||
	fail "the backend cannot read the credential it needs"
if docker compose -p web exec -T frontend cat /run/secrets/db_password >/dev/null 2>&1; then
	fail "the frontend was given a credential it has no use for"
fi
info "the credential reaches the backend and not the frontend"

step "changing one tier's port leaves the other tiers alone"
# The container ids before and after are the evidence: `config set` re-creates
# the services the parameter declares and nothing else. Bouncing the whole
# project on every parameter change would make an operator hesitate to use it.
before_backend=$(docker compose -p web ps -q backend)
before_db=$(docker compose -p web ps -q db)

"${MORZER}" --root "${WEB_ROOT}" config set http_port="${WEB_MOVED_PORT}"

[ "$(web_port frontend)" = "${WEB_MOVED_PORT}" ] ||
	fail "the frontend port did not move to ${WEB_MOVED_PORT}"
[ "$(curl -fsS "http://127.0.0.1:${WEB_MOVED_PORT}/health/ready")" = "frontend" ] ||
	fail "nothing answers on the frontend's new port"
[ "$(docker compose -p web ps -q backend)" = "${before_backend}" ] ||
	fail "changing http_port re-created the backend, which does not use it"
[ "$(docker compose -p web ps -q db)" = "${before_db}" ] ||
	fail "changing http_port re-created the database, which does not use it"
[ "$(web_port backend)" = "${WEB_API_PORT}" ] ||
	fail "the backend's own port moved when the frontend's changed"
info "the frontend moved; the backend and database were untouched"

step "the three-tier example: doctor and journal"
"${MORZER}" --root "${WEB_ROOT}" doctor
grep -q '"type":"config"' "${WEB_ROOT}/var/lib/web/manager/operations.jsonl" ||
	fail "the parameter change was not journaled"
info "three tiers, three parameters, one journal"

# The fleet view's actual case: two installations, one target
#
# Everything above published one row, which proves the mechanism and not the
# design -- a fleet of one is a `status` command with extra steps. This second
# installation is a genuinely different one, on a different root, running a
# different product, and it publishes to the same place.
step "a second installation publishes to the same target"
"${MORZER}" --root "${WEB_ROOT}" backup target add "file://${FLEET_TARGET}" >/dev/null ||
	fail "cannot add the fleet target to the three-tier installation"
"${MORZER}" --root "${WEB_ROOT}" --json fleet publish --target "file://${FLEET_TARGET}" \
	>"${WORK}/fleet-web.json" ||
	fail "the three-tier installation could not publish: $(cat "${WORK}/fleet-web.json")"

"${MORZER}" --root "${ROOT}" --json fleet ls "file://${FLEET_TARGET}" >"${WORK}/fleet-both.json" ||
	fail "fleet ls failed with two rows: $(cat "${WORK}/fleet-both.json")"
jq -e '.data.rows | length == 2' "${WORK}/fleet-both.json" >/dev/null ||
	fail "fleet ls found $(jq -r '.data.rows | length' "${WORK}/fleet-both.json") row(s), expected 2"
jq -e '[.data.rows[].product] | sort == ["demo","web"]' "${WORK}/fleet-both.json" >/dev/null ||
	fail "the two rows are not the two installations: $(jq -c '[.data.rows[].product]' "${WORK}/fleet-both.json")"

# Read from the `demo` machine, about a machine it has no other knowledge of.
# That is the whole feature, so it is worth printing rather than only asserting.
step "two machines, read from one of them"
"${MORZER}" --root "${ROOT}" fleet ls "file://${FLEET_TARGET}" ||
	fail "fleet ls failed in plain mode with two rows"

# ----------------------------------------------------------------------------
# The roster: absence, and a key the roster does not name
#
# Everything above is a reader with no anchor. It can say a signature is
# *there* and never that it checks out, and an installation that stopped
# publishing is structurally invisible to it -- listing a prefix shows exactly
# the population that is fine. Both need one file, and it is one file because
# they are one fact.

step "a roster, built the way the documentation says to build one"
# The documented recipe, run against the real binary on both machines. A dry
# run prints the row a machine would publish, and all three fields of a roster
# entry are in it -- `installation describe` deliberately does not carry the
# key, because that document is desired state and a signing key is machine
# identity (RFC 0027, RFC 0028 §5.3).
roster_entry() {
	"${MORZER}" --root "$1" --json fleet publish --dry-run --target "file://${FLEET_TARGET}" |
		jq -r '"  - product: " + .data.row.product,
		       "    id: " + .data.row.installation_id,
		       "    key: " + .data.row.signing_key'
}
{
	printf 'schema: 1\ninstallations:\n'
	roster_entry "${ROOT}"
	roster_entry "${WEB_ROOT}"
} >"${WORK}/roster.yaml"
grep -q 'key: RW' "${WORK}/roster.yaml" ||
	fail "the roster carries no signing key: $(cat "${WORK}/roster.yaml")"

step "with a roster, a row is verified rather than merely signed"
"${MORZER}" --root "${ROOT}" --json fleet ls "file://${FLEET_TARGET}" \
	--expect "${WORK}/roster.yaml" >"${WORK}/fleet-verified.json" ||
	fail "fleet ls with a roster failed: $(cat "${WORK}/fleet-verified.json")"
jq -e '[.data.rows[] | select(.signature == "verified")] | length == 2' \
	"${WORK}/fleet-verified.json" >/dev/null ||
	fail "the rows were not verified against the roster: $(jq -c '[.data.rows[].signature]' "${WORK}/fleet-verified.json")"
jq -e '.data.expected == 2 and ([.data.rows[] | select(.absent)] | length == 0)' \
	"${WORK}/fleet-verified.json" >/dev/null ||
	fail "the roster's own count is wrong: $(jq -c '.data' "${WORK}/fleet-verified.json")"
# The anchor is a file the operator maintains, and a reader that stopped saying
# so would be presenting its own input back as evidence.
jq -e '(.data.limitations | length) > 0' "${WORK}/fleet-verified.json" >/dev/null ||
	fail "a reader with a roster printed a table with no statement at all"

step "an installation that never published is the row the roster exists for"
# The answer no listing can produce: an object that was never written cannot
# announce itself. This entry also binds no key, which is allowed and which the
# reader has to say out loud.
{
	cat "${WORK}/roster.yaml"
	printf '  - product: demo\n    id: inst_01ACCEPTANCEWENTQUIET\n'
} >"${WORK}/roster-gone.yaml"
"${MORZER}" --root "${ROOT}" --json fleet ls "file://${FLEET_TARGET}" \
	--expect "${WORK}/roster-gone.yaml" >"${WORK}/fleet-absent.json" && {
	fail "fleet ls exited zero with an installation missing from the fleet"
}
jq -e '[.data.rows[] | select(.absent)] | length == 1' "${WORK}/fleet-absent.json" >/dev/null ||
	fail "the absent installation was not a row: $(jq -c '[.data.rows[] | {product, absent}]' "${WORK}/fleet-absent.json")"
jq -e '.data.limitations | join(" ") | contains("binds no key")' \
	"${WORK}/fleet-absent.json" >/dev/null ||
	fail "the reader did not say which installations it cannot authenticate"

step "a row signed by a key the roster does not name"
# The scenario RFC 0026 decision 6b lives or dies by, arranged from the reader's
# side: the roster binds the *other* machine's key to this installation, so the
# row fails against the anchor and verifies against the key it carries. That is
# what an overwrite by another machine looks like from here -- and it is also
# what a roster with two keys transposed looks like, which this reader cannot
# tell apart and does not pretend to.
DEMO_ID=$(jq -r '.data.row.installation_id' "${WORK}/fleet-publish.json")
WEB_KEYLINE=$(jq -r '.data.row.signing_key' "${WORK}/fleet-web.json")
jq -r --arg id "${DEMO_ID}" --arg key "${WEB_KEYLINE}" \
	'"schema: 1", "installations:", "  - product: demo", "    id: " + $id, "    key: " + $key' \
	<<<'{}' >"${WORK}/roster-wrong.yaml"
"${MORZER}" --root "${ROOT}" --json fleet ls "file://${FLEET_TARGET}" \
	--expect "${WORK}/roster-wrong.yaml" >"${WORK}/fleet-wrongkey.json" && {
	fail "fleet ls exited zero on a row it could not anchor"
}
jq -e '[.data.rows[] | select(.signature == "signed-by-another-key")] | length == 1' \
	"${WORK}/fleet-wrongkey.json" >/dev/null ||
	fail "the row was not named as signed by an unnamed key: $(jq -c '[.data.rows[].signature]' "${WORK}/fleet-wrongkey.json")"
# The refusal, not the verdict: a verifier anchored in the row would have
# reported this one as good, because the row carries the key that signed it.
jq -e '[.data.rows[] | select(.signature == "verified")] | length == 0' \
	"${WORK}/fleet-wrongkey.json" >/dev/null ||
	fail "a row was verified against the key it carries"
jq -e '[.data.rows[] | select(.signature == "signed-by-another-key") | .row] | all(. == null)' \
	"${WORK}/fleet-wrongkey.json" >/dev/null ||
	fail "the payload of a row that failed verification was rendered anyway"

step "a signature removed is not the same as a machine that never had one"
# The downgrade. An attacker who cannot forge a signature can delete one, and if
# a stripped signature read as the ordinary unsigned state, removing the
# .minisig beside a forged row would be enough to escape the roster.
mv "${FLEET_TARGET}/${FLEET_KEY}.minisig" "${WORK}/held.minisig"
"${MORZER}" --root "${ROOT}" --json fleet ls "file://${FLEET_TARGET}" \
	--expect "${WORK}/roster.yaml" >"${WORK}/fleet-stripped.json" && {
	fail "fleet ls exited zero on a row whose signature was removed"
}
jq -e '[.data.rows[] | select(.signature == "missing-signature")] | length == 1' \
	"${WORK}/fleet-stripped.json" >/dev/null ||
	fail "a stripped signature read as an ordinary unsigned row: $(jq -c '[.data.rows[].signature]' "${WORK}/fleet-stripped.json")"
mv "${WORK}/held.minisig" "${FLEET_TARGET}/${FLEET_KEY}.minisig"

# And once in the form an operator sees, which is the output the documentation
# quotes. A sample nobody captured from a running binary is a sample that drifts
# the first time a column moves.
step "the fleet, with a roster, as an operator sees it"
# The status is captured rather than discarded. `|| true` accepts *any* failure,
# so a malformed roster, an unreadable target or a panic in the renderer would
# all have read as the deliberate one -- and this is the only step that exercises
# the plain rendering at all, so nothing else would have caught it either.
fleet_plain_status=0
"${MORZER}" --root "${ROOT}" fleet ls "file://${FLEET_TARGET}" \
	--expect "${WORK}/roster-gone.yaml" >"${WORK}/fleet-plain.txt" ||
	fleet_plain_status=$?
cat "${WORK}/fleet-plain.txt"

# 3 is the preflight status: one installation is deliberately absent. A 2 here
# would be the roster being refused, which is a different scenario passing under
# this one's name.
[ "${fleet_plain_status}" -eq 3 ] ||
	fail "plain fleet ls exited ${fleet_plain_status}, expected 3 for an absent installation"

# The table is counted by what the target holds, and the absent installation is
# a line in it rather than a number in the headline.
grep -q "^2 row(s) on file://${FLEET_TARGET}$" "${WORK}/fleet-plain.txt" ||
	fail "the headline is not the two rows the target holds: $(head -1 "${WORK}/fleet-plain.txt")"
for expected in \
	"the roster expects this installation; no target holds a row" \
	"the roster expects 3 installation(s); 2 published a row and 1 did not"; do
	grep -qF "${expected}" "${WORK}/fleet-plain.txt" ||
		fail "the plain rendering lost: ${expected}"
done
info "plain fleet ls renders the absent installation and exits ${fleet_plain_status}"

# The P4 timer is deliberately not exercised here: this scenario runs
# `init --install-units=false`, so the machine manages no units at all and
# reconciliation correctly does nothing. What the timer contains is asserted
# against the rendered unit text in the systemd adapter's own tests, and that it
# arrives with the first target and leaves with the last is asserted against the
# supervisor port in the suite. Neither needs Docker, and a step here that ran
# `doctor` and printed a sentence would read like a check without being one.

step "the journal recorded every operation"
status_field '.data.last_operation.type'
for op in init apply backup restore update; do
	grep -q "\"type\":\"${op}\"" "${ROOT}/var/lib/demo/manager/operations.jsonl" ||
		fail "no ${op} in the journal"
done
info "init, apply, backup, restore, update and rollback are all journaled"

# ----------------------------------------------------------------------------
# A bundle that carries its own images, installed with no registry at all
#
# RFC 0011 P4. Everything above proves the lifecycle against a registry that is
# up; this proves the one case the feature exists for -- a customer who cannot
# reach the vendor's registry, because for some of them that is simply true.
#
# The registry is stopped rather than merely ignored, and the local copy of the
# bundled image is deleted first. Both are necessary: a scenario that left
# either in place would pass on a machine where the image happened to be
# present, which is every machine that has just run the steps above.

step "the earlier deployment comes down first"
# The air-gapped bundle is derived from the same source and so carries the same
# Compose project name. Converging over the running 1.3.0 would leave this
# scenario asserting against somebody else's containers.
docker compose -p demo down --remove-orphans >/dev/null 2>&1 || true
info "the demo project is down"

step "packing app into a bundle, and leaving db to be pulled"
BUNDLED="${WORK}/bundle-bundled"
prepare_bundle testdata/bundle "${BUNDLED}"

# Only `app` travels. The mixed case is the design -- a vendor ships their two
# private images and keeps pulling postgres -- and it is the one that neither a
# bundle-everything nor a bundle-nothing scenario can show.
sed -i \
	-e "s|^  app: .*|  app:\n    ref: ${REGISTRY}/demo/app@${APP_DIGEST}\n    from: bundle|" \
	"${BUNDLED}/manifest.yaml"
grep -q '    from: bundle' "${BUNDLED}/manifest.yaml" ||
	fail "the manifest rewrite did not take; nothing below would be testing bundling"

"${MORZER}" release pack "${BUNDLED}"
[ -f "${BUNDLED}/images/index.json" ] || fail "pack wrote no image layout"
info "$(find "${BUNDLED}/images" -type f | wc -l) file(s) under images/"

step "release verify accepts the packed bundle"
"${MORZER}" release verify "${BUNDLED}"

step "the registry goes away, and so does the local copy of app"
docker rm -f "${REGISTRY_NAME}" >/dev/null 2>&1 || true
STARTED_REGISTRY=0
if curl -fsS "http://${REGISTRY}/v2/" >/dev/null 2>&1; then
	fail "the registry is still answering, so this proves nothing"
fi
# Every local reference to the image the bundle carries. Without this the
# converge below could succeed on a copy the earlier steps left behind.
docker rmi "${REGISTRY}/demo/app:acceptance" >/dev/null 2>&1 || true
docker rmi "${REGISTRY}/demo/app@${APP_DIGEST}" >/dev/null 2>&1 || true
if docker image inspect "${REGISTRY}/demo/app@${APP_DIGEST}" >/dev/null 2>&1; then
	fail "the app image is still local, so the install would not be proving anything"
fi
info "registry stopped, app gone from the local store"

step "init installs it with no registry to reach"
AIR_ROOT="${WORK}/airgapped-root"
"${MORZER}" --root "${AIR_ROOT}" init \
	--release "${BUNDLED}" \
	--profile embedded \
	--domain airgapped.example \
	--no-recovery-recipient \
	--install-units=false \
	--set http_port="${AIRGAP_HTTP_PORT}" \
	--set log_level=debug

"${MORZER}" --root "${AIR_ROOT}" apply

running=$(docker compose -p demo ps --status running --format '{{.Name}}' | wc -l)
[ "${running}" = "2" ] ||
	fail "expected 2 running container(s) after the air-gapped install, found ${running}"
info "2 container(s) running, with no registry in existence"

step "the deployment runs the alias, not the reference no daemon can resolve"
image=$(docker compose -p demo ps --format '{{.Image}}' | grep -F 'demo/app' | head -1)
case "${image}" in
*:morzer-sha256-*) info "app runs as ${image}" ;;
*) fail "app is running as ${image}, which is not the alias ingest creates" ;;
esac
# The other half, and the one that would rot silently: the reference the
# manifest pins must still not resolve. If this ever starts passing, the
# measurement RFC 0011 decision 19 rests on has changed, and the design can be
# revisited rather than worked around.
if docker image inspect "${REGISTRY}/demo/app@${APP_DIGEST}" >/dev/null 2>&1; then
	fail "the manifest's own reference resolves locally, which contradicts decision 19"
fi
info "the manifest reference still resolves nowhere, as measured"

step "doctor is content with an installation that pulls nothing it cannot reach"
"${MORZER}" --root "${AIR_ROOT}" --json doctor >"${WORK}/airgapped-doctor.json" 2>&1 ||
	fail "doctor failed on the air-gapped installation: $(cat "${WORK}/airgapped-doctor.json")"
jq -e '[.data.results[] | select(.id == "images.bundled")] | length == 1 and .[0].status == "ok"' \
	"${WORK}/airgapped-doctor.json" >/dev/null ||
	fail "doctor did not report the bundled images as loaded: $(jq -c '.data.results[] | select(.id == "images.bundled")' "${WORK}/airgapped-doctor.json")"
info "images.bundled is ok"

step "tearing the air-gapped deployment down"
docker compose -p demo down --remove-orphans >/dev/null 2>&1 || true
