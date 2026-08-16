#!/usr/bin/env bash
# file: scripts/run-mutation.sh
# version: 1.0.0
# guid: 2a7c9e14-8b05-4d63-9f27-1e4a6b03d85f
# last-edited: 2026-08-16
#
# Runs gremlins mutation testing inside a disk budget it cannot escape.
#
# ── WHY THIS WRAPPER EXISTS ────────────────────────────────────────────────────
#
# On 2026-08-16 `gremlins unleash --dry-run ./internal/auth/` filled a 926GB
# volume and panicked. The cause was not that mutation testing is expensive:
#
#   gremlins copies the ENTIRE module directory once per worker.
#
# This module's root is 34GB, of which 28GB is `.worktrees/` -- sixteen sibling
# git worktrees that live INSIDE the module root. At the default worker count
# (runtime.NumCPU()) that is 340-400GB of copies for a package with a handful of
# mutants. It happened on a --dry-run because gremlins stages the working copy
# before it decides not to run tests; "dry" describes test execution, not the
# filesystem.
#
# Three independent guards, because each covers a case the others do not:
#
#   1. REFUSE to run from the primary checkout. Run from a worktree and the copy
#      is 1.8GB instead of 34GB -- a 19x reduction from geometry alone, before
#      any budget applies. This is the fix; 2 and 3 are the net beneath it.
#   2. OWN the scratch directory. TMPDIR is pointed at a directory this script
#      created, so every byte gremlins writes is somewhere we can delete
#      unconditionally on exit -- including SIGINT and SIGTERM.
#   3. WATCHDOG the free space. A background loop kills the run and empties the
#      scratch directory the moment free space crosses the floor, rather than
#      letting the volume fill and take out every other process on the machine.
#
# Guard 3 is the one that matters when the estimate is wrong, which is the only
# time a guard is worth having.

set -euo pipefail

PKG="${PKG:-}"
if [[ -z "${PKG}" ]]; then
    echo "PKG is required, e.g. PKG=./internal/scanner/ $0" >&2
    exit 2
fi

# Tunables. Defaults are deliberately conservative: this tool has already taken
# a machine down once.
WORKERS="${MUTATE_WORKERS:-2}"
MIN_FREE_GB="${MUTATE_MIN_FREE_GB:-60}"   # refuse to start below this
FLOOR_GB="${MUTATE_FLOOR_GB:-20}"         # abort mid-run below this
POLL_SECONDS="${MUTATE_POLL_SECONDS:-5}"

REPO_ROOT="$(git rev-parse --show-toplevel)"

# ── Guard 1: never from the primary checkout ──────────────────────────────────
#
# `git rev-parse --git-dir` prints a path under .git/worktrees/ inside a linked
# worktree, and a plain ".git" in the primary checkout. That is the cheapest
# reliable discriminator, and it does not depend on directory naming.
GIT_DIR="$(git rev-parse --git-dir)"
if [[ "${GIT_DIR}" != *"/worktrees/"* ]]; then
    cat >&2 <<EOF
Refusing to run mutation testing from the primary checkout.

gremlins copies the whole module directory once per worker. This root is
$(du -shx "${REPO_ROOT}" 2>/dev/null | cut -f1) because .worktrees/ lives inside it, so N workers means N copies of
all of it. That is what filled the disk on 2026-08-16.

Run from a worktree instead:
    git worktree add .worktrees/mutate -b chore/mutate-\$(date +%s)
    cd .worktrees/mutate && make mutate PKG=${PKG}
EOF
    exit 3
fi

# ── Guard 2: own every byte the tool writes ───────────────────────────────────
SCRATCH="$(mktemp -d "${TMPDIR:-/tmp}/abk-mutate.XXXXXX")"
export TMPDIR="${SCRATCH}"
export GOTMPDIR="${SCRATCH}"

WATCHDOG_PID=""
GREMLINS_PID=""

# free_gb reports whole GB available on the volume holding $1.
# df -k is used rather than -h so the number is parseable rather than "1.8Ti".
free_gb() {
    df -k "$1" | awk 'NR==2 { printf "%d", $4 / 1024 / 1024 }'
}

cleanup() {
    local rc=$?
    [[ -n "${WATCHDOG_PID}" ]] && kill "${WATCHDOG_PID}" 2>/dev/null || true
    [[ -n "${GREMLINS_PID}" ]] && kill "${GREMLINS_PID}" 2>/dev/null || true
    if [[ -d "${SCRATCH}" ]]; then
        # Unconditional: the whole point of creating this directory ourselves is
        # that deleting it can never be a judgement call.
        rm -rf "${SCRATCH}"
    fi
    return ${rc}
}
trap cleanup EXIT INT TERM

# ── Preflight ─────────────────────────────────────────────────────────────────
avail="$(free_gb "${SCRATCH}")"
if (( avail < MIN_FREE_GB )); then
    echo "Refusing to start: ${avail}GB free, need ${MIN_FREE_GB}GB." >&2
    echo "   Override with MUTATE_MIN_FREE_GB=<gb> once you know the copy size." >&2
    exit 4
fi

module_size_kb="$(du -skx "${REPO_ROOT}" 2>/dev/null | cut -f1)"
module_size_gb=$(( module_size_kb / 1024 / 1024 ))
projected=$(( module_size_gb * WORKERS ))
echo "mutation run: PKG=${PKG} workers=${WORKERS}"
echo "  module ${module_size_gb}GB x ${WORKERS} workers ~= ${projected}GB peak"
echo "  free ${avail}GB, abort floor ${FLOOR_GB}GB"
echo "  scratch ${SCRATCH}"

if (( projected > avail - FLOOR_GB )); then
    echo "Projected ${projected}GB exceeds the usable budget ($(( avail - FLOOR_GB ))GB)." >&2
    echo "   Lower MUTATE_WORKERS, or free space first." >&2
    exit 5
fi

# ── Guard 3: watchdog ─────────────────────────────────────────────────────────
(
    while sleep "${POLL_SECONDS}"; do
        now="$(free_gb "${SCRATCH}" 2>/dev/null || echo 999)"
        if (( now < FLOOR_GB )); then
            echo "" >&2
            echo "DISK FLOOR HIT: ${now}GB free (< ${FLOOR_GB}GB). Killing mutation run." >&2
            [[ -n "${GREMLINS_PID}" ]] && kill -9 "${GREMLINS_PID}" 2>/dev/null || true
            rm -rf "${SCRATCH:?}"/* 2>/dev/null || true
            exit 0
        fi
    done
) &
WATCHDOG_PID=$!

# Run gremlins in the background so the watchdog has a PID to kill, then wait on
# it. `wait` propagates the exit status, so a failed mutation run still fails the
# make target.
gremlins unleash --workers "${WORKERS}" "$@" "${PKG}" &
GREMLINS_PID=$!

status=0
wait "${GREMLINS_PID}" || status=$?

final="$(free_gb "${SCRATCH}" 2>/dev/null || echo 0)"
echo "mutation run finished (exit ${status}); ${final}GB free, scratch removed"
exit ${status}
