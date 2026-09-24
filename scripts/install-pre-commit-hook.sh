#!/bin/sh
# Installs a git pre-commit hook that runs `make lint` (gofmt check +
# golangci-lint), the same command CI's lint workflow runs, so lint and
# format failures are caught before a commit instead of after a push.
#
#   scripts/install-pre-commit-hook.sh              install (or refresh) the hook
#   scripts/install-pre-commit-hook.sh --force      overwrite a hook we didn't write
#   scripts/install-pre-commit-hook.sh --uninstall  remove the hook (only if ours)
#
# Dev-only tooling; it is not part of the gage binary and is excluded from
# coverage (see .testcoverage.yml and codecov.yml).

set -eu

MARKER='# gage-managed pre-commit hook'

force=0
uninstall=0
for arg in "$@"; do
	case "$arg" in
	--force) force=1 ;;
	--uninstall) uninstall=1 ;;
	-h | --help)
		sed -n '2,9p' "$0" | sed 's/^# \{0,1\}//'
		exit 0
		;;
	*)
		echo "install-pre-commit-hook: unknown argument: $arg" >&2
		exit 2
		;;
	esac
done

root="$(git rev-parse --show-toplevel)"
cd "$root"

# --git-path honours worktrees and core.hooksPath; it may be relative to
# the repo root, which is where we are now.
hook="$(git rev-parse --git-path hooks/pre-commit)"

is_ours() {
	[ -f "$hook" ] && grep -qF "$MARKER" "$hook"
}

if [ "$uninstall" -eq 1 ]; then
	if [ ! -e "$hook" ]; then
		echo "no pre-commit hook at $hook; nothing to do"
	elif is_ours; then
		rm "$hook"
		echo "removed $hook"
	else
		echo "install-pre-commit-hook: $hook was not installed by this script; leaving it alone" >&2
		exit 1
	fi
	exit 0
fi

if [ -e "$hook" ] && ! is_ours && [ "$force" -ne 1 ]; then
	echo "install-pre-commit-hook: $hook already exists and was not installed by this script." >&2
	echo "Re-run with --force to overwrite it." >&2
	exit 1
fi

mkdir -p "$(dirname "$hook")"
cat >"$hook" <<EOF
#!/bin/sh
$MARKER
# Installed by scripts/install-pre-commit-hook.sh. Runs the same lint and
# format checks as CI's lint workflow.
cd "\$(git rev-parse --show-toplevel)" || exit 1
if ! make lint; then
	echo >&2
	echo "pre-commit: make lint failed; commit aborted." >&2
	echo "Run 'make fmt' to fix formatting, or bypass once with: git commit --no-verify" >&2
	exit 1
fi
EOF
chmod +x "$hook"
echo "installed $hook (runs 'make lint' before each commit)"
