#!/usr/bin/env bash
# The acceptance scenario on rootless Podman, for the quadlet runtime (RFC 0023
# P4).
#
# acceptance.sh proves the lifecycle against real Docker. This proves the same
# manager against the other runtime it drives: Quadlet units under the user's
# own systemd manager, on rootless Podman, with no daemon and no root.
#
#   init → apply → config set (the port moves, the container is re-created)
#        → backup → update → rollback → restore
#
# The release is testdata/bundle-quadlet, testdata/bundle's product declaring
# quadlet instead of Compose. Its images travel in the bundle, so `init` and
# `update` load them and no unit pulls: the throwaway registry is stopped, and
# the local copies removed, before anything is installed.
#
# It runs under `--root`, as acceptance.sh does, and as the user. The units
# live in the user's runtime directory whatever the root, so the scenario
# refuses to start when an installation of `demo` already has units there.
#
#   just acceptance-podman   # needs podman, a user systemd manager, sops, jq
#
set -euo pipefail

ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
MORZER="${MORZER:-${ROOT_DIR}/morzer}"
MORZER="$(cd "$(dirname "${MORZER}")" && pwd)/$(basename "${MORZER}")"

# Its own port, so this registry and acceptance.sh's can run side by side.
REGISTRY="localhost:${ACCEPTANCE_PODMAN_REGISTRY_PORT:-5001}"
# Named for this run, so a cleanup only ever removes its own.
REGISTRY_NAME="morzer-acceptance-podman-registry-$$"

# Not the bundle's default, and not acceptance.sh's ports either, so a failure
# naming a port names the run it came from.
HTTP_PORT="${ACCEPTANCE_PODMAN_HTTP_PORT:-18089}"
MOVED_PORT="${ACCEPTANCE_PODMAN_MOVED_PORT:-18088}"

WORK="$(cd "$(mktemp -d -t morzer-acceptance-podman-XXXXXX)" && pwd)"
ROOT="${WORK}/root"

STARTED_REGISTRY=0
INSTALLED=0

step() { printf '\n\033[1m== %s ==\033[0m\n' "$*"; }
info() { printf '   %s\n' "$*"; }
fail() {
	printf '\n\033[31merror: %s\033[0m\n' "$*" >&2
	exit 1
}

# cleanup takes down what this run installed, and only that: a run refused for
# finding someone else's units never touches them.
cleanup() {
	local status=$?

	step "cleaning up"
	if [ "${INSTALLED}" = 1 ]; then
		local id
		id=$("${MORZER}" --root "${ROOT}" --json status 2>/dev/null | jq -r '.data.installation_id' || true)
		if [ -n "${id}" ] && [ "${id}" != null ]; then
			"${MORZER}" --root "${ROOT}" installation remove --force --purge --confirm "${id}" >/dev/null 2>&1 || true
		fi
		# What a failed remove leaves, so the next run is not refused for it.
		# Every name is one the preconditions found free.
		systemctl --user stop "${UNITS[@]}" >/dev/null 2>&1 || true
		rm -rf -- "${UNIT_DIR}"
		systemctl --user daemon-reload >/dev/null 2>&1 || true
		systemctl --user reset-failed "${UNITS[@]}" >/dev/null 2>&1 || true
		podman rm -f "${CONTAINERS[@]}" >/dev/null 2>&1 || true
		podman volume rm -f "${VOLUMES[@]}" >/dev/null 2>&1 || true
	fi
	if [ "${STARTED_REGISTRY}" = 1 ]; then
		podman rm -f "${REGISTRY_NAME}" >/dev/null 2>&1 || true
	fi
	if [ -n "${KEEP_WORK:-}" ]; then
		info "keeping ${WORK}"
	else
		rm -rf -- "${WORK}"
	fi

	if [ $status -eq 0 ]; then
		printf '\n\033[32macceptance on podman passed\033[0m\n'
	fi
	return $status
}
trap cleanup EXIT

require_tool() {
	command -v "$1" >/dev/null 2>&1 || fail "$1 is required and not installed"
}

# ----------------------------------------------------------------------------
# Preconditions

step "checking prerequisites"
[ "$(uname -s)" = Linux ] || fail "the quadlet runtime exists on Linux only"
[ "$(id -u)" != 0 ] || fail "run it as a user: root cannot run the quadlet runtime"
for tool in podman systemctl sops jq curl; do require_tool "${tool}"; done
[ -x "${MORZER}" ] || fail "no morzer binary at ${MORZER}; run \`just build\` first"
[ -n "${XDG_RUNTIME_DIR:-}" ] || fail "XDG_RUNTIME_DIR is not set; log in as the user, or enable lingering"
systemctl --user show-environment >/dev/null 2>&1 ||
	fail "no systemd manager of your own answers; enable lingering with \`loginctl enable-linger\`"
# What an installation of demo owns on this machine, whatever its root. A
# reboot clears the unit directory and keeps the volumes, so each is checked.
UNIT_DIR="${XDG_RUNTIME_DIR}/containers/systemd/morzer/demo"
UNITS=(demo-app.service demo-db.service demo-uploads-volume.service demo-database-volume.service)
CONTAINERS=(systemd-demo-app systemd-demo-db)
VOLUMES=(systemd-demo-uploads systemd-demo-database)
[ ! -e "${UNIT_DIR}" ] ||
	fail "${UNIT_DIR} exists: an installation of demo has units here, and this run would replace them"
for name in "${CONTAINERS[@]}"; do
	! podman container exists "${name}" || fail "the container ${name} exists, and this run would replace it"
done
for name in "${VOLUMES[@]}"; do
	! podman volume exists "${name}" || fail "the volume ${name} exists, and this run would remove it"
done
info "$(podman --version)"
info "$(systemctl --version | head -1)"
info "$(sops --version 2>&1 | head -1)"
info "work directory ${WORK}"

# The volume helper, decided the way the manager decides it (see acceptance.sh):
# a backup refuses when it is not in the store, and a runner starts with none.
# MORZER_VOLUME_HELPER_IMAGE wins when set, trimmed, as the manager trims it.
HELPER_IMAGE="${MORZER_VOLUME_HELPER_IMAGE:-}"
HELPER_IMAGE="${HELPER_IMAGE#"${HELPER_IMAGE%%[![:space:]]*}"}"
HELPER_IMAGE="${HELPER_IMAGE%"${HELPER_IMAGE##*[![:space:]]}"}"
if [ -z "${HELPER_IMAGE}" ]; then
	HELPER_IMAGE="$(sed -n 's/^const DefaultHelperImage = "\(.*\)"$/\1/p' "${ROOT_DIR}/internal/adapters/runtime/compose/volumes.go")"
	[ -n "${HELPER_IMAGE}" ] || fail "cannot read DefaultHelperImage from the source"
fi
if ! podman image exists "${HELPER_IMAGE}"; then
	info "pulling volume helper ${HELPER_IMAGE}"
	podman pull --quiet "${HELPER_IMAGE}" >/dev/null ||
		fail "cannot pull the volume helper ${HELPER_IMAGE}"
fi

# ----------------------------------------------------------------------------
# Helpers

status_field() {
	"${MORZER}" --root "${ROOT}" --json status | jq -r "$1"
}

container_id() {
	podman inspect --format '{{.Id}}' systemd-demo-app
}

# published reads the host port the app's 18080 is published on.
published() {
	podman port systemd-demo-app 18080/tcp 2>/dev/null | head -1 | sed 's/.*://'
}

in_app() {
	"${MORZER}" --root "${ROOT}" exec app -- "$@"
}

in_db() {
	"${MORZER}" --root "${ROOT}" exec db -- "$@"
}

# assert_active waits for both services' units, which a start reports before
# the container's health has settled.
assert_active() {
	local deadline=$(($(date +%s) + 60))
	until systemctl --user is-active --quiet demo-app.service demo-db.service; do
		[ "$(date +%s)" -lt "${deadline}" ] ||
			fail "demo-app and demo-db are not both active after 60s: $(systemctl --user is-active demo-app.service demo-db.service | tr '\n' ' ')"
		sleep 1
	done
	info "demo-app and demo-db are active"
}

answers() {
	curl -fsS --max-time 5 "http://127.0.0.1:$1/health/ready" >/dev/null 2>&1
}

# ----------------------------------------------------------------------------
# A registry, so the stubs have digests, then bundles that carry them

step "starting a throwaway registry at ${REGISTRY}"
# A port already taken fails the run here, before anything is installed.
podman run -d --rm --name "${REGISTRY_NAME}" \
	-p "127.0.0.1:${REGISTRY##*:}:5000" docker.io/library/registry:2 >/dev/null
STARTED_REGISTRY=1
for _ in $(seq 1 30); do
	curl -fsS "http://${REGISTRY}/v2/" >/dev/null 2>&1 && break
	sleep 1
done
curl -fsS "http://${REGISTRY}/v2/" >/dev/null 2>&1 || fail "the registry did not become ready"

# push_stub builds a stub, pushes it, and prints the digest the registry gave
# it. The base image is named in full, since Podman resolves a short name only
# where registries.conf says how. Podman hands a build the shell's proxy
# variables, and a proxy on the host's loopback is not on the build's.
push_stub() {
	local name=$1 tag base
	tag="${REGISTRY}/demo/${name}:acceptance"
	base="$(sed -n 's/^FROM //p' "${ROOT_DIR}/testdata/acceptance/${name}/Dockerfile" | head -1)"
	podman build --quiet --http-proxy=false --from "docker.io/library/${base}" -t "${tag}" \
		"${ROOT_DIR}/testdata/acceptance/${name}" >/dev/null
	podman push --quiet --tls-verify=false --digestfile "${WORK}/${name}.digest" "${tag}" >/dev/null
	cat "${WORK}/${name}.digest"
}

step "building and pushing the stub images"
APP_DIGEST=$(push_stub app)
DB_DIGEST=$(push_stub db)
info "app ${APP_DIGEST}"
info "db  ${DB_DIGEST}"

# prepare_bundle copies the fixture, pins both images to bundle, and, given a
# version, makes it that release's successor in its version alone.
prepare_bundle() {
	local dest=$1 version=${2:-}
	cp -r "${ROOT_DIR}/testdata/bundle-quadlet" "${dest}"
	sed -i \
		-e "s|^  app: .*|  app:\n    ref: ${REGISTRY}/demo/app@${APP_DIGEST}\n    from: bundle|" \
		-e "s|^  db: .*|  db:\n    ref: ${REGISTRY}/demo/db@${DB_DIGEST}\n    from: bundle|" \
		"${dest}/manifest.yaml"
	[ "$(grep -c '    from: bundle' "${dest}/manifest.yaml")" = 2 ] ||
		fail "the manifest in ${dest} was not rewritten to bundle both images"
	if [ -n "${version}" ]; then
		sed -i "s|^  version: .*|  version: ${version}|" "${dest}/manifest.yaml"
		printf '%s\n' "${version}" >"${dest}/VERSION"
	fi
	"${MORZER}" release pack "${dest}" >/dev/null
	"${MORZER}" release verify "${dest}" >/dev/null
}

step "preparing the bundles"
prepare_bundle "${WORK}/bundle-1.2.0"
prepare_bundle "${WORK}/bundle-1.2.1" 1.2.1
info "1.2.0 and 1.2.1 carry both images"

step "the registry goes away, and so do the local copies"
podman rm -f "${REGISTRY_NAME}" >/dev/null 2>&1 || true
STARTED_REGISTRY=0
podman rmi -f "${REGISTRY}/demo/app:acceptance" "${REGISTRY}/demo/db:acceptance" >/dev/null 2>&1 || true
for ref in "${REGISTRY}/demo/app@${APP_DIGEST}" "${REGISTRY}/demo/db@${DB_DIGEST}"; do
	! podman image exists "${ref}" || fail "${ref} is still in the store, so the install would prove nothing"
done
info "only the bundles hold the images"

# ----------------------------------------------------------------------------
# Scenario

step "init"
mkdir -p "${WORK}/keys"
RECOVERY=$("${MORZER}" secret recipients generate-recovery-key "${WORK}/keys/recovery.key" | tail -1)
"${MORZER}" --root "${ROOT}" init \
	--release "${WORK}/bundle-1.2.0" \
	--domain acceptance.example \
	--recovery-recipient "${RECOVERY}" \
	--install-units=false \
	--set http_port="${HTTP_PORT}" \
	--set log_level=debug
# From here this run owns what the preconditions found free.
INSTALLED=1

step "apply"
"${MORZER}" --root "${ROOT}" apply
[ -f "${UNIT_DIR}/demo-app.container" ] || fail "apply installed no Quadlet unit in ${UNIT_DIR}"
assert_active
[ "$(published)" = "${HTTP_PORT}" ] || fail "published on $(published), expected ${HTTP_PORT}"
answers "${HTTP_PORT}" || fail "nothing answers on ${HTTP_PORT}"
info "app answers on ${HTTP_PORT}"
[ "$(in_app printenv DEMO_LOG_LEVEL)" = debug ] || fail "the log_level parameter did not reach the container"
info "DEMO_LOG_LEVEL=debug inside the container"

step "config set re-creates the container on the new port"
# A port is fixed when a container is created, so a restart would keep the old
# mapping and report success (RFC 0007). The container has to be a new one.
before=$(container_id)
"${MORZER}" --root "${ROOT}" config set http_port="${MOVED_PORT}"
assert_active
after=$(container_id)
[ "${after}" != "${before}" ] || fail "config set kept the container ${before}; the port cannot have moved"
[ "$(published)" = "${MOVED_PORT}" ] || fail "config set left the port at $(published), expected ${MOVED_PORT}"
answers "${MOVED_PORT}" || fail "nothing answers on ${MOVED_PORT}"
! answers "${HTTP_PORT}" || fail "${HTTP_PORT} still answers after the move"
info "a new container answers on ${MOVED_PORT}, and ${HTTP_PORT} is free"

step "backup"
"${MORZER}" --root "${ROOT}" backup target add "file://${ROOT}/offsite"
# One marker for each way a backup reaches data: the hook's data directory,
# a volume captured hot, and one captured cold with its service stopped.
echo acceptance-marker >"${ROOT}/var/lib/demo/data/marker"
in_app sh -c 'echo acceptance-marker > /var/lib/demo/uploads/marker'
in_db sh -c 'echo acceptance-marker > /var/lib/postgresql/data/marker'
"${MORZER}" --root "${ROOT}" backup --reason acceptance
assert_active
"${MORZER}" --root "${ROOT}" backup verify --remote
info "a backup holding the three markers is on the target, and both services run again"

step "update to 1.2.1"
"${MORZER}" --root "${ROOT}" update "${WORK}/bundle-1.2.1"
assert_active
[ "$(status_field '.data.current_release.version')" = 1.2.1 ] ||
	fail "status reports $(status_field '.data.current_release.version') after the update"
[ "$(in_app printenv DEMO_VERSION)" = 1.2.1 ] || fail "the container still runs $(in_app printenv DEMO_VERSION)"
answers "${MOVED_PORT}" || fail "nothing answers on ${MOVED_PORT} after the update"
info "1.2.1 runs, on the port that was set"

step "rollback to 1.2.0"
"${MORZER}" --root "${ROOT}" rollback
assert_active
[ "$(status_field '.data.current_release.version')" = 1.2.0 ] ||
	fail "status reports $(status_field '.data.current_release.version') after the rollback"
[ "$(in_app printenv DEMO_VERSION)" = 1.2.0 ] || fail "the container still runs $(in_app printenv DEMO_VERSION)"
info "1.2.0 runs again"

step "restore"
in_app rm -f /var/lib/demo/uploads/marker
in_db rm -f /var/lib/postgresql/data/marker
rm -f -- "${ROOT}/var/lib/demo/data/marker"
"${MORZER}" --root "${ROOT}" restore --force --confirm "$(status_field '.data.installation_id')"
assert_active
[ "$(cat "${ROOT}/var/lib/demo/data/marker")" = acceptance-marker ] ||
	fail "the restore did not bring the data directory back"
[ "$(in_app cat /var/lib/demo/uploads/marker)" = acceptance-marker ] ||
	fail "the restore did not bring the uploads volume back"
[ "$(in_db cat /var/lib/postgresql/data/marker)" = acceptance-marker ] ||
	fail "the restore did not bring the database volume back"
answers "${MOVED_PORT}" || fail "nothing answers on ${MOVED_PORT} after the restore"
info "the three markers are back, and the app answers"

step "doctor, after everything"
"${MORZER}" --root "${ROOT}" doctor
