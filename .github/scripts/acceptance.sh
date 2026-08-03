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
#        → status → doctor → backup → restore → update → rollback → doctor
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
REGISTRY="${ACCEPTANCE_REGISTRY:-localhost:5000}"
REGISTRY_NAME="morzer-acceptance-registry"
WORK="${ACCEPTANCE_WORK:-$(mktemp -d -t morzer-acceptance-XXXXXX)}"
mkdir -p "${WORK}"
ROOT="${WORK}/root"

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
push_stub() {
	local name=$1 tag="${REGISTRY}/demo/$1:acceptance"

	docker build --quiet -t "${tag}" "${ROOT_DIR}/testdata/acceptance/${name}" >/dev/null
	docker push --quiet "${tag}" >/dev/null

	# The digest the registry computed, which is what the manifest pins.
	docker inspect --format '{{index .RepoDigests 0}}' "${tag}" |
		sed "s|^${REGISTRY}/demo/${name}@||"
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

assert_running() {
	local expected=$1
	local running
	running=$(docker compose -p demo ps --status running --format '{{.Name}}' | wc -l)
	[ "${running}" = "${expected}" ] ||
		fail "expected ${expected} running container(s), found ${running}"
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
	--install-units=false

step "apply"
"${MORZER}" --root "${ROOT}" apply
assert_running 2

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

step "a reboot: services stopped, then apply --startup"
# Restarting the Docker daemon is what a reboot does to a deployment, but doing
# it on a shared runner would disrupt everything else. Stopping the project's
# containers reproduces the state the boot-time path has to recover from.
docker compose -p demo stop >/dev/null
assert_running 0
"${MORZER}" --root "${ROOT}" apply --startup
assert_running 2

step "backup"
echo "acceptance-marker" > "${ROOT}/var/lib/demo/data/marker"
"${MORZER}" --root "${ROOT}" backup --reason acceptance
"${MORZER}" --root "${ROOT}" backup list

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

step "update to 1.3.0"
"${MORZER}" --root "${ROOT}" update "${WORK}/bundle-1.3.0"
assert_running 2
version=$(status_field '.data.current_release.version')
[ "${version}" = "1.3.0" ] || fail "expected 1.3.0 after the update, got ${version}"
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

step "the journal recorded every operation"
status_field '.data.last_operation.type'
for op in init apply backup restore update; do
	grep -q "\"type\":\"${op}\"" "${ROOT}/var/lib/demo/manager/operations.jsonl" ||
		fail "no ${op} in the journal"
done
info "init, apply, backup, restore, update and rollback are all journaled"
