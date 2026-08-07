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
REGISTRY="${ACCEPTANCE_REGISTRY:-localhost:5000}"
REGISTRY_NAME="morzer-acceptance-registry"
WORK="${ACCEPTANCE_WORK:-$(mktemp -d -t morzer-acceptance-XXXXXX)}"
mkdir -p "${WORK}"
ROOT="${WORK}/root"

# Deliberately not the bundle's default of 18080. The whole point of the
# parameter stages below is that a port the operator chose is the one that gets
# published, checked for conflicts and probed -- and a test that used the
# default would pass whether or not any of that worked.
HTTP_PORT="${ACCEPTANCE_HTTP_PORT:-18099}"

# A second port, so `config set` is proved to *move* a published port rather
# than merely to agree with one already in place.
MOVED_PORT="${ACCEPTANCE_MOVED_PORT:-18098}"

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

step "the journal recorded every operation"
status_field '.data.last_operation.type'
for op in init apply backup restore update; do
	grep -q "\"type\":\"${op}\"" "${ROOT}/var/lib/demo/manager/operations.jsonl" ||
		fail "no ${op} in the journal"
done
info "init, apply, backup, restore, update and rollback are all journaled"
