#!/bin/sh
# Install a published morzer release.
#
#   curl -fsSL https://morzecrew.github.io/morzer/install.sh | sh -s -- --version 1.4.0
#
# The long form this replaces is still documented, because the long form is what
# this script does and an operator who wants to check its work needs to be able
# to: see https://morzecrew.github.io/morzer/get-started/installation/.
#
# POSIX sh, no bashisms -- the target is a freshly provisioned Linux box where
# /bin/sh may be dash or busybox, which is exactly the machine this exists for.
# `just shellcheck` enforces it with the sh dialect.
#
# It verifies before it installs, it never runs sudo on its own initiative, and
# it writes exactly one file unless it says otherwise. RFC 0022 is the design.

set -eu

REPO="morzecrew/morzer"
BINARY="morzer"
API="https://api.github.com/repos/${REPO}"
DOWNLOAD="https://github.com/${REPO}/releases/download"

# The signing key, embedded rather than fetched.
#
# A script that downloads the key it verifies against has verified that the
# server was self-consistent and nothing else. Embedding also gives this
# something to say when a release is signed by a different key, which is the
# event the documentation already tells operators to stop for.
#
# It is `morzer.pub` at the repository root, byte for byte, and a test asserts
# that -- a key that drifted from the one the pipeline signs with would reject
# every release, which is loud, but only at install time.
PUBKEY='untrusted comment: minisign public key 6244CB37DB91DD52
RWRS3ZHbN8tEYuF9Se2e+JzQMiUCoLbABbJtzSxBThI/U4Bhw+AR+IbQ'

# The kernel a Go 1.25 static binary needs. Warned about, never enforced: the
# binary may well run, and refusing on a number read out of `uname -r` would
# refuse containers whose kernel string is the host's.
MIN_KERNEL_MAJOR=3
MIN_KERNEL_MINOR=2

# Narration goes to stderr; stdout carries only what a caller would capture --
# the --print-only report, the block when it is printed rather than written, and
# the final summary. A progress line in a runbook's variable is a bug.
note() { printf '%s\n' "$*" >&2; }
warn() { printf 'warning: %s\n' "$*" >&2; }
report() { printf '%s\n' "$*"; }

die() {
	printf 'error: %s\n' "$*" >&2
	exit 1
}

usage() {
	cat <<'EOF'
Install a published morzer release.

Usage: install.sh [options]

  --version X.Y.Z        Release to install. Default: the latest published
                         release, resolved once and then printed. A prerelease
                         is installed when named and never when inferred.
  --dir PATH             Install prefix. Default: /usr/local/bin when writable,
                         otherwise $HOME/.local/bin.
  --digest sha256:HEX    Expected checksum of the archive. Checked before
                         SHA256SUMS; a mismatch is fatal.
  --require-signature    Refuse to install when minisign is absent or the
                         signature does not verify.
  --no-verify-signature  Skip the signature check, and say so.
  --no-modify-path       Never edit a shell startup file; print what to add.
  --completions          Install shell completions even when not interactive.
  --no-completions       Do not install shell completions.
  --shell NAME           Override the detected shell (bash, zsh or fish).
  --print-only           Print what was detected and what would happen; change
                         nothing.
  -h, --help             This.

Environment: MORZER_VERSION and MORZER_INSTALL_DIR are read when the matching
flag is absent, for the `curl | sh` form where passing flags means `sh -s --`.
EOF
}

# ---------------------------------------------------------------------------
# Arguments

opt_version="${MORZER_VERSION:-}"
opt_dir="${MORZER_INSTALL_DIR:-}"
opt_digest=""
opt_shell=""
opt_require_signature=no
opt_verify_signature=yes
opt_modify_path=yes
opt_completions=auto
opt_print_only=no

# need_value keeps `--version` at the end of a command line from silently
# consuming nothing and installing "latest" instead of what the runbook meant.
need_value() {
	[ "$#" -ge 2 ] || die "$1 needs a value"
	[ -n "$2" ] || die "$1 needs a value"
}

while [ "$#" -gt 0 ]; do
	case "$1" in
	--version) need_value "$1" "${2:-}" && opt_version="$2" && shift ;;
	--version=*) opt_version="${1#*=}" ;;
	--dir) need_value "$1" "${2:-}" && opt_dir="$2" && shift ;;
	--dir=*) opt_dir="${1#*=}" ;;
	--digest) need_value "$1" "${2:-}" && opt_digest="$2" && shift ;;
	--digest=*) opt_digest="${1#*=}" ;;
	--shell) need_value "$1" "${2:-}" && opt_shell="$2" && shift ;;
	--shell=*) opt_shell="${1#*=}" ;;
	--require-signature) opt_require_signature=yes ;;
	--no-verify-signature) opt_verify_signature=no ;;
	--no-modify-path) opt_modify_path=no ;;
	--completions) opt_completions=yes ;;
	--no-completions) opt_completions=no ;;
	--print-only) opt_print_only=yes ;;
	-h | --help)
		usage
		exit 0
		;;
	*) die "unknown option: $1 (try --help)" ;;
	esac
	shift
done

# Before anything is downloaded, so a runbook that inherited both flags learns
# it is not verifying at the moment it runs rather than the moment it is
# audited. Letting argument order decide whether verification happens is how a
# check stops happening silently.
if [ "$opt_require_signature" = yes ] && [ "$opt_verify_signature" = no ]; then
	die "--require-signature and --no-verify-signature contradict each other"
fi

case "$opt_digest" in
"") ;;
sha256:*)
	opt_digest="${opt_digest#sha256:}"
	;;
*) die "--digest must look like sha256:<hex>" ;;
esac

# ---------------------------------------------------------------------------
# Detection (RFC 0022 §5.2)
#
# What bears on which archive to fetch and whether the result will be usable.
# Everything else about the machine is `morzer doctor`'s, which runs a minute
# later and is thorough. A bootstrap script that grows into a system audit is a
# second doctor nobody maintains.

os="$(uname -s 2>/dev/null || echo unknown)"
case "$os" in
Linux) ;;
Darwin)
	# The refusal stands; the advice under it is what changed. It used to
	# say "build from source" while the tree did not compile for darwin at
	# all, so an hour of somebody's evening went into a suggestion that
	# could not work. A refusal that tells you what to do instead has to be
	# right about it.
	#
	# It is careful about what that buys, too: a binary that builds and
	# runs. Whether it can drive a deployment against Docker Desktop is not
	# something anyone has run yet, and promising it here would be the same
	# defect one release later.
	die "this installs Linux builds only, and there is no macOS build to point
       at: the release matrix is linux/amd64 and linux/arm64.

       Building from source does work: \`go build ./cmd/morzer\` with Go 1.25
       or newer produces a binary that runs on macOS. That is a CLI for
       authoring bundles and looking around -- running a deployment is a
       Linux server's job, and this installer will not pretend otherwise."
	;;
*) die "unsupported operating system: ${os} (this installs Linux builds only)" ;;
esac

machine="$(uname -m 2>/dev/null || echo unknown)"
case "$machine" in
x86_64 | amd64) arch=amd64 ;;
aarch64 | arm64) arch=arm64 ;;
*)
	die "unsupported architecture: ${machine} (published builds are amd64 and arm64)"
	;;
esac

# Refused by name rather than guessed: guessing downloads an archive whose
# binary the kernel will not exec, and that surfaces several steps later with a
# much worse message.

kernel="$(uname -r 2>/dev/null || echo unknown)"
kernel_major="${kernel%%.*}"
kernel_rest="${kernel#*.}"
kernel_minor="${kernel_rest%%.*}"
kernel_warning=""
case "$kernel_major" in
'' | *[!0-9]*) ;;
*)
	case "$kernel_minor" in
	'' | *[!0-9]*) kernel_minor=0 ;;
	esac
	if [ "$kernel_major" -lt "$MIN_KERNEL_MAJOR" ] ||
		{ [ "$kernel_major" -eq "$MIN_KERNEL_MAJOR" ] &&
			[ "$kernel_minor" -lt "$MIN_KERNEL_MINOR" ]; }; then
		kernel_warning="kernel ${kernel} is below the ${MIN_KERNEL_MAJOR}.${MIN_KERNEL_MINOR} a Go binary needs"
	fi
	;;
esac

# $SHELL holds a path; every consumer here wants the name -- the startup file
# for PATH and the argument for `completion install`, which knows bash, zsh and
# fish and nothing else. Normalised once, here.
if [ -n "$opt_shell" ]; then
	shell="$opt_shell"
elif [ -n "${SHELL:-}" ]; then
	shell="$(basename "$SHELL")"
else
	shell=""
fi
case "$shell" in
bash | zsh | fish) shell_known=yes ;;
*) shell_known=no ;;
esac

# An unrecognised shell is not fatal. The binary still installs and the script
# prints what to add by hand, which is the same answer `completion install`
# gives for a shell it cannot place a file for.

# The prefix: what was asked for, then the system one when it can be written,
# then the user one. Resolved before anything is printed, because the PATH block
# and every message name the prefix that was actually chosen.
if [ -n "$opt_dir" ]; then
	dir="$opt_dir"
elif [ -d /usr/local/bin ] && [ -w /usr/local/bin ]; then
	dir=/usr/local/bin
else
	dir="${HOME:-}/.local/bin"
	[ -n "${HOME:-}" ] || die "cannot resolve \$HOME; name a prefix with --dir"
fi

# ${PATH:-} rather than ${PATH}: with `set -u` an unset PATH aborts here, three
# lines before the check that would have said "neither curl nor wget is on
# PATH". `env -i sh install.sh` and a systemd unit with no PATH= both hit it,
# and "PATH: parameter not set" is not a diagnostic anybody can act on.
# The prefix is written into a shell startup file and into a fish drop-in, and
# from then on it is code that shell runs at every start. A quote, a `$`, a
# backtick or a newline in it produces a file that is malformed at best and
# executes something at worst -- on the file an operator is least likely to
# suspect and most likely to keep for years.
#
# Refused rather than escaped: quoting this correctly for two shells is a lot
# of care spent on a prefix nobody has. Spaces are deliberately allowed, since
# they are the one case that does occur and the generated block handles them.
case "$dir" in
*[\"\'\$\`\\]* | *"
"*)
	die "the install prefix contains a character this cannot safely write into
       a shell startup file: ${dir}
       Quotes, \$, backticks, backslashes and newlines are refused. Choose a
       plainer --dir, or pass --no-modify-path and add it yourself."
	;;
esac

case ":${PATH:-}:" in
*":${dir}:"*) on_path=yes ;;
*) on_path=no ;;
esac

# The ordinary trap: installing into ~/.local/bin on a machine that already has
# /usr/local/bin/morzer, where the old one goes on answering to the name.
shadowed_by=""
if command -v "$BINARY" >/dev/null 2>&1; then
	found="$(command -v "$BINARY")"
	[ "$found" = "${dir}/${BINARY}" ] || shadowed_by="$found"
fi

if [ -t 1 ]; then interactive=yes; else interactive=no; fi

if [ "$opt_completions" = auto ]; then
	# On at a terminal, off in a Dockerfile or a CI job: somebody running an
	# installer by hand wants the tool to work properly, and a build does
	# not want writes into a home directory that belongs to it.
	if [ "$interactive" = yes ] && [ "$shell_known" = yes ]; then
		completions=yes
	else
		completions=no
	fi
else
	completions="$opt_completions"
fi

# ---------------------------------------------------------------------------
# The tools this needs

if command -v curl >/dev/null 2>&1; then
	downloader=curl
elif command -v wget >/dev/null 2>&1; then
	downloader=wget
else
	die "neither curl nor wget is on PATH"
fi

if command -v sha256sum >/dev/null 2>&1; then
	hasher=sha256sum
elif command -v shasum >/dev/null 2>&1; then
	hasher=shasum
else
	die "neither sha256sum nor shasum is on PATH; cannot verify a download"
fi

# zstd is not optional in the way it looks. GNU tar's --zstd runs the `zstd`
# binary as a filter, so a machine without it cannot extract the archive even
# though its tar accepts the flag; busybox and bsdtar decompress internally and
# do not need it. There is no probe that tells the two apart without an archive
# in hand, so this is recorded rather than enforced: it appears in --print-only,
# and the refusal at extraction time names both ways out.
if command -v zstd >/dev/null 2>&1; then
	zstd_present=yes
else
	zstd_present=no
fi

fetch() {
	# --proto '=https' so a redirect cannot walk the download onto plain
	# http, which is the one place a checksum served beside the file it
	# describes would not help.
	if [ "$downloader" = curl ]; then
		curl -fsSL --proto '=https' --tlsv1.2 -o "$2" "$1"
	else
		wget -q -O "$2" "$1"
	fi
}

sha256_of() {
	if [ "$hasher" = sha256sum ]; then
		sha256sum "$1" | awk '{print $1}'
	else
		shasum -a 256 "$1" | awk '{print $1}'
	fi
}

# ---------------------------------------------------------------------------
# Which release

resolve_latest() {
	# /releases/latest never returns a prerelease, which is the rule this
	# project wants: a prerelease is admissible when an operator names it
	# and never when a script inferred it.
	resolve_tmp="$(mktemp)"
	if ! fetch "${API}/releases/latest" "$resolve_tmp"; then
		rm -f "$resolve_tmp"
		die "cannot reach the release API; name a version with --version"
	fi
	resolve_tag="$(sed -n 's/.*"tag_name"[[:space:]]*:[[:space:]]*"\([^"]*\)".*/\1/p' \
		"$resolve_tmp" | head -n 1)"
	rm -f "$resolve_tmp"
	[ -n "$resolve_tag" ] || die "the release API returned no tag_name; name a version with --version"
	printf '%s\n' "$resolve_tag"
}

if [ -n "$opt_version" ]; then
	version="${opt_version#v}"
	resolved_from=argument
else
	version="$(resolve_latest)"
	version="${version#v}"
	resolved_from=api
fi

tag="v${version}"
archive="${BINARY}_${version}_linux_${arch}.tar.zst"
archive_url="${DOWNLOAD}/${tag}/${archive}"
sums_url="${DOWNLOAD}/${tag}/SHA256SUMS"
sig_url="${DOWNLOAD}/${tag}/SHA256SUMS.minisig"
target="${dir}/${BINARY}"

if command -v minisign >/dev/null 2>&1; then
	minisign_present=yes
else
	minisign_present=no
fi

signature_plan="verify with minisign"
if [ "$opt_verify_signature" = no ]; then
	signature_plan="skipped (--no-verify-signature)"
elif [ "$minisign_present" = no ]; then
	if [ "$opt_require_signature" = yes ]; then
		signature_plan="required, and minisign is not installed"
	else
		signature_plan="skipped: minisign is not installed"
	fi
fi

# ---------------------------------------------------------------------------
# --print-only
#
# Everything detected, not only what would be fetched: this is what makes §5.2
# testable without a download, and what a nightly job runs against the real API
# to catch an asset name drifting.

if [ "$opt_print_only" = yes ]; then
	report "os              ${os}"
	report "arch            ${machine} -> ${arch}"
	report "kernel          ${kernel}${kernel_warning:+  (${kernel_warning})}"
	if [ "$shell_known" = yes ]; then
		report "shell           ${shell}"
	else
		report "shell           ${shell:-unknown} (not one this script writes a block for)"
	fi
	report "interactive     ${interactive}"
	report "version         ${version} (${resolved_from})"
	report "archive         ${archive}"
	report "url             ${archive_url}"
	report "install to      ${target}"
	report "already on PATH ${on_path}"
	report "signature       ${signature_plan}"
	report "completions     ${completions}"
	if [ "$zstd_present" = no ]; then
		report "zstd            not installed (extraction needs tar's own zstd support)"
	fi
	if [ -n "$opt_digest" ]; then
		report "digest          ${opt_digest} (from --digest)"
	fi
	if [ -n "$shadowed_by" ]; then
		report "shadowed by     ${shadowed_by}"
	fi
	exit 0
fi

if [ "$opt_require_signature" = yes ] && [ "$minisign_present" = no ]; then
	die "--require-signature was given and minisign is not on PATH"
fi

# ---------------------------------------------------------------------------
# Fetch and verify (RFC 0022 §5.3)

# Removed on every exit path including failure: a partial archive left in /tmp
# is the file somebody finds later and extracts.
work="$(mktemp -d)"
cleanup() { rm -rf "$work"; }
trap cleanup EXIT HUP INT TERM

note "downloading ${archive} (${version})"
fetch "$archive_url" "${work}/${archive}" ||
	die "cannot download ${archive_url}
       check the version exists: https://github.com/${REPO}/releases"

actual_digest="$(sha256_of "${work}/${archive}")"

# The caller's digest first: somebody who names one is asserting something
# about specific bytes and must not be told about a checksum file first.
if [ -n "$opt_digest" ]; then
	if [ "$actual_digest" != "$opt_digest" ]; then
		die "--digest does not match what was downloaded
       expected ${opt_digest}
       actual   ${actual_digest}"
	fi
	note "digest matches the one you pinned"
fi

fetch "$sums_url" "${work}/SHA256SUMS" || die "cannot download ${sums_url}"

# The archive's own line, never `sha256sum -c --ignore-missing`. SHA256SUMS
# covers every architecture, so checking it whole fails on the archives that
# were not downloaded -- and --ignore-missing, the usual way around that,
# reports OK when *no* archive is present at all. That is the failure this
# script exists to remove, and it is what the old instructions did.
# Read as fields rather than matched as a pattern: an archive name is full of
# dots, and in a regular expression a dot matches anything. Comparing the name
# field for equality is both simpler and exact.
expected_digest=""
while read -r sums_digest sums_name; do
	# `sha256sum` writes "<hash>  <name>" in text mode and "<hash> *<name>"
	# in binary mode; both appear in the wild.
	sums_name="${sums_name#\*}"
	if [ "$sums_name" = "$archive" ]; then
		expected_digest="$sums_digest"
		break
	fi
done <"${work}/SHA256SUMS"

[ -n "$expected_digest" ] ||
	die "SHA256SUMS has no line for ${archive}
       the release exists but does not carry this archive"

if [ "$actual_digest" != "$expected_digest" ]; then
	die "checksum mismatch for ${archive}
       expected ${expected_digest}
       actual   ${actual_digest}"
fi
note "checksum matches SHA256SUMS"

if [ "$opt_verify_signature" = no ]; then
	warn "not verifying the signature (--no-verify-signature)"
elif [ "$minisign_present" = no ]; then
	warn "minisign is not installed, so the checksum file's signature was not
         checked. Install minisign and re-run with --require-signature to
         make this fatal."
else
	if ! fetch "$sig_url" "${work}/SHA256SUMS.minisig"; then
		if [ "$opt_require_signature" = yes ]; then
			die "cannot download ${sig_url} and --require-signature was given"
		fi
		warn "no signature published at ${sig_url}"
	else
		printf '%s\n' "$PUBKEY" >"${work}/morzer.pub"
		if minisign -Vm "${work}/SHA256SUMS" -p "${work}/morzer.pub" \
			>"${work}/minisign.log" 2>&1; then
			note "signature verifies against the key this script carries"
		else
			sed 's/^/       /' "${work}/minisign.log" >&2 || true
			die "the signature does not verify against the key this script carries.
       That means the release was signed by a different key than the one
       this script was published with. Stop and check the release page
       before installing anything: https://github.com/${REPO}/releases"
		fi
	fi
fi

# ---------------------------------------------------------------------------
# Extract and check what came out

if tar --zstd -xf "${work}/${archive}" -C "$work" 2>/dev/null; then
	:
elif [ "$zstd_present" = yes ]; then
	zstd -dc "${work}/${archive}" | tar -xf - -C "$work"
else
	die "cannot extract ${archive}.
       This tar cannot decompress zstd on its own and the zstd binary is not
       installed -- GNU tar's --zstd runs it as a filter. Install zstd (apt
       install zstd, apk add zstd, dnf install zstd) and run this again."
fi

[ -f "${work}/${BINARY}" ] || die "the archive does not contain a ${BINARY} binary"
chmod 0755 "${work}/${BINARY}"

# A correct download of the wrong thing is the failure a checksum cannot catch,
# because the checksum came from the same place.
reported="$("${work}/${BINARY}" version 2>/dev/null | head -n 1 || true)"
case "$reported" in
*"$version"*) ;;
"") die "the downloaded binary does not run on this machine (${machine})" ;;
*) die "the downloaded binary reports \"${reported}\", not ${version}" ;;
esac

# ---------------------------------------------------------------------------
# Install (RFC 0022 §5.6)

if [ -L "$target" ]; then
	die "${target} is a symlink to $(readlink "$target")
       Refusing to write through it -- that is usually a package manager's
       tree. Remove it, or install somewhere else with --dir."
fi
if [ -e "$target" ] && [ ! -f "$target" ]; then
	die "${target} exists and is not a regular file
       Refusing to replace it. Install somewhere else with --dir."
fi

if [ ! -d "$dir" ]; then
	mkdir -p "$dir" 2>/dev/null || die "cannot create ${dir}
       Create it yourself, or choose a writable prefix with --dir."
fi

# Never sudo. A script fetched over the network that escalates on its own
# initiative is the thing operators are right to distrust; it says what to run
# instead and exits non-zero.
if [ ! -w "$dir" ]; then
	die "${dir} is not writable by this user.
       Re-run with elevation:
         curl -fsSL https://morzecrew.github.io/morzer/install.sh | sudo sh -s -- --version ${version} --dir ${dir}
       or install into your own prefix:
         --dir \$HOME/.local/bin"
fi

# Written beside the target and renamed onto it: a rename within one directory
# is atomic, and copying over a binary that is currently running fails with
# ETXTBSY where replacing it does not.
staged="${dir}/.${BINARY}.install.$$"
cp "${work}/${BINARY}" "$staged" || die "cannot write into ${dir}"
chmod 0755 "$staged"
mv -f "$staged" "$target" || {
	rm -f "$staged"
	die "cannot replace ${target}"
}

# ---------------------------------------------------------------------------
# PATH (RFC 0022 §5.4)
#
# The smallest edit that survives a new shell, in a marked block, in one file
# per shell, printed before it is written.

BEGIN_MARK="# >>> morzer >>>"
END_MARK="# <<< morzer <<<"

posix_block() {
	printf '%s\n' "$BEGIN_MARK"
	printf '%s\n' "# Added by morzer's install.sh. Remove this block to undo it."
	# shellcheck disable=SC2016 # $PATH is literal here: it is expanded by the
	# shell that reads the startup file, not by this one.
	printf 'case ":$PATH:" in *":%s:"*) ;; *) PATH="%s:$PATH" ;; esac\n' "$dir" "$dir"
	printf '%s\n' "export PATH"
	printf '%s\n' "$END_MARK"
}

fish_block() {
	printf '%s\n' "$BEGIN_MARK"
	printf '%s\n' "# Added by morzer's install.sh. Remove this file to undo it."
	# Single-quoted: an unquoted path with a space becomes two arguments and
	# fish_add_path adds neither of them. The guard above has already refused
	# the characters that single quotes cannot carry.
	printf "fish_add_path '%s'\\n" "$dir"
	printf '%s\n' "$END_MARK"
}

# A POSIX `case ... esac` in a .fish file is a syntax error at every subsequent
# shell start, which is why the block is generated in the target shell's syntax
# rather than translated.
block_for_shell() {
	if [ "$shell" = fish ]; then fish_block; else posix_block; fi
}

path_files_written=""
path_block_printed=no

print_block_once() {
	[ "$path_block_printed" = no ] || return 0
	path_block_printed=yes
	report ""
	report "Add this to your shell's startup file so ${dir} is on PATH:"
	report ""
	block_for_shell
}

# write_block appends the block to one file, or explains why it did not.
#
# Idempotent by marker: re-running finds the block and leaves it alone. Without
# the markers an operator who runs the installer three times gets three copies,
# which is the defect every installer of this shape eventually has.
write_block() {
	wb_file="$1"

	if [ -e "$wb_file" ] && grep -q "^${BEGIN_MARK}\$" "$wb_file" 2>/dev/null; then
		note "${wb_file} already has the morzer block"
		return 0
	fi

	# A symlink into a dotfiles repository means the file is generated;
	# appending to it loses the edit at the next sync, silently.
	if [ -L "$wb_file" ]; then
		warn "${wb_file} is a symlink (dotfiles are managed elsewhere), so it was not edited"
		print_block_once
		return 0
	fi
	if [ -e "$wb_file" ] && [ ! -w "$wb_file" ]; then
		warn "${wb_file} is not writable, so it was not edited"
		print_block_once
		return 0
	fi
	if [ ! -e "$wb_file" ] && [ ! -w "$(dirname "$wb_file")" ]; then
		warn "$(dirname "$wb_file") is not writable, so ${wb_file} was not created"
		print_block_once
		return 0
	fi

	{
		printf '\n'
		block_for_shell
	} >>"$wb_file" || {
		warn "could not write ${wb_file}"
		print_block_once
		return 0
	}
	path_files_written="${path_files_written}${path_files_written:+, }${wb_file}"
}

# Where it goes, by shell. Both the login file and the interactive one for bash
# and zsh: neither covers the other, so a single file leaves half the sessions
# without the prefix -- which is the failure that looks like "the installer
# didn't work". ~/.zshenv is deliberately never used: it is read by every zsh
# including non-interactive ones, and a PATH prepend there reaches scripts that
# never asked for it.
edit_startup_files() {
	home="${HOME:-}"
	[ -n "$home" ] || {
		warn "\$HOME is not set, so no startup file was edited"
		print_block_once
		return 0
	}

	case "$shell" in
	fish)
		# A drop-in: no existing file is edited, and removing the file
		# is a complete uninstall.
		fish_dir="${XDG_CONFIG_HOME:-${home}/.config}/fish/conf.d"
		mkdir -p "$fish_dir" 2>/dev/null || true
		write_block "${fish_dir}/morzer.fish"
		;;
	bash)
		# bash reads the first of ~/.bash_profile, ~/.bash_login and
		# ~/.profile and stops, so ~/.profile is the login file only
		# when neither of the others exists.
		if [ -f "${home}/.bash_profile" ]; then
			write_block "${home}/.bash_profile"
		elif [ -f "${home}/.bash_login" ]; then
			write_block "${home}/.bash_login"
		else
			write_block "${home}/.profile"
		fi
		if [ -f "${home}/.bashrc" ]; then
			write_block "${home}/.bashrc"
		fi
		;;
	zsh)
		write_block "${ZDOTDIR:-$home}/.zshrc"
		if [ -f "${ZDOTDIR:-$home}/.zprofile" ]; then
			write_block "${ZDOTDIR:-$home}/.zprofile"
		fi
		;;
	*)
		note "shell ${shell:-unknown} is not one this script writes a block for"
		print_block_once
		;;
	esac
	return 0
}

path_action=none
if [ "$on_path" = yes ]; then
	path_action="already on PATH"
elif [ "$opt_modify_path" = no ]; then
	path_action="not modified (--no-modify-path)"
	print_block_once
else
	edit_startup_files
	if [ -n "$path_files_written" ]; then
		path_action="added to ${path_files_written}"
	else
		path_action="printed above; nothing was edited"
	fi
fi

# ---------------------------------------------------------------------------
# Completions (RFC 0022 §5.5)
#
# By running the binary that was just installed, never by writing a completion
# from here. Where a shell reads completions from is knowledge that belongs in
# one implementation, versioned with the binary and tested against a fake HOME.
# A second copy written in sh would drift, and it would drift silently: a
# completion in the wrong directory produces no error, just a Tab key that does
# nothing.

completion_action="skipped"
if [ "$completions" = yes ]; then
	if [ "$shell_known" = yes ]; then
		if "$target" completion install "$shell" >/dev/null 2>&1; then
			completion_action="installed for ${shell}"
		else
			# Never fatal. The binary is on the machine and works;
			# a completion that could not be written is a warning
			# naming the command to retry.
			warn "could not install the ${shell} completion. The binary is installed;
         run \`${target} completion install ${shell}\` to see why."
			completion_action="failed (the install is fine)"
		fi
	else
		note "run \`${target} completion install\` for shell completions"
		completion_action="skipped: shell ${shell:-unknown} not recognised"
	fi
fi

# ---------------------------------------------------------------------------
# What just happened

report ""
report "morzer ${version} installed"
report "  path      ${target}"
report "  sha256    ${actual_digest}"
report "  PATH      ${path_action}"
report "  completions ${completion_action}"

if [ -n "$kernel_warning" ]; then
	warn "$kernel_warning"
fi
if [ -n "$shadowed_by" ]; then
	warn "${shadowed_by} comes first on your PATH, so \`morzer\` still runs that one.
         Installed: ${target}"
fi
if [ -n "$path_files_written" ]; then
	# A PATH edit that does not affect the shell you are standing in is the
	# other thing everyone has seen.
	note "open a new terminal, or run \`exec \$SHELL\`, to pick up the new PATH"
fi

note "next: \`${BINARY} doctor\` checks what this machine needs to run a deployment"
