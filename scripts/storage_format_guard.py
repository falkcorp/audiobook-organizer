#!/usr/bin/env python3
# file: scripts/storage_format_guard.py
# version: 1.1.0
# guid: 35696d63-9d8e-4fc3-b6a8-9c9770cd798c
# last-edited: 2026-10-04

"""Storage-format guard for `make rollback`.

`make rollback` swaps the previous binary (`<bin>.prev`) back in. That is only
safe when the previous binary can read the store: once a release converts the
store to a newer storage format, a binary swap cannot go back, and an older
binary started on converted rows would misread them (design
docs/design/2026-10-03-storage-efficiency-design.md, section 9).

The Makefile collects these strings over ssh and passes them here:

  --prev        stdout of `<bin>.prev --print-storage-format` (empty or an error
                text for every build older than the flag; those all read format 1)
  --sidecar     content of `<db>.storage-format`, the store's format
  --checkpoint  content of `<db>.migration-checkpoint`, the checkpoint_dir the
                storage cut-over recorded (empty when none)
  --current     stdout of `<bin> --print-storage-format` (the binary being
                rolled back), used only under ROLLBACK_IGNORE_FORMAT=1
  --*-err       the stderr of the ssh command that produced each value, so a
                refusal names its cause (sudo needing a password, no such file)

Exit 0: the swap may proceed. Exit 1: refuse; nothing is swapped. The one
override, ROLLBACK_IGNORE_FORMAT=1, only covers an unreadable sidecar, and even
then the guard refuses when the current binary reports a format above the
previous binary's. It never overrides a store that is newer than the previous
binary.

Standard library only. Tests: scripts/test_storage_format_guard.py.
"""

import argparse
import os
import re
import sys

RUNBOOK = "docs/system/runbooks.md#storage-format-restore"


def _int_or_none(text):
    """Return the whole stripped string as a format number (an int >= 1), or None.

    Anything else is unreadable: empty, an error text (which can contain
    digits), or 0, which no build ever prints. This matches the Go side, which
    rejects a sidecar below 1.
    """
    text = (text or "").strip()
    if text and text.isascii() and text.isdigit() and int(text) >= 1:
        return int(text)
    return None


def _cause(err_text, read_text=None):
    """Return a short cause: the last few non-empty stderr lines, plus what was read.

    read_text is the stdout that failed to parse; when it is non-empty it is
    quoted, so a sidecar holding "1 2" says so instead of "no error output".
    """
    lines = [ln.strip() for ln in (err_text or "").splitlines() if ln.strip()]
    parts = []
    if (read_text or "").strip():
        parts.append(f"read {read_text.strip()!r}, not one integer >= 1")
    if lines:
        parts.append("stderr: " + " | ".join(lines[-3:]))
    if not parts:
        return "no output and no error output (the file may be missing or empty)"
    return "; ".join(parts)


# What every build older than --print-storage-format prints (cobra), on stderr.
_OLD_BUILD_FLAG_ERROR = re.compile(r"unknown flag: --print-storage-format(\s|$)")


def _current_format(current_text, current_err):
    """Return (format, reason) for the binary being rolled back, or (None, reason).

    Only two answers are trusted: a format number on stdout, or cobra's
    old-build "unknown flag: --print-storage-format" error on stderr (format 1).
    A crash, an ssh failure or anything else is unknown, never "1".
    """
    n = _int_or_none(current_text)
    if n is not None:
        return n, f"it printed {n}"
    if _OLD_BUILD_FLAG_ERROR.search(current_err or ""):
        return 1, "it predates --print-storage-format, so it reads format 1"
    return None, _cause(current_err, current_text)


def restore_steps(db, bin_path, store, checkpoint):
    """Return the restore-from-checkpoint steps as a list of lines."""
    base = os.path.basename(os.path.normpath(db))
    pre_format_bin = f"{bin_path}.pre-format-{store}"
    lines = ["", "Restore from the pre-migration checkpoint instead:"]
    lines.append("  1. sudo systemctl stop audiobook-organizer.service")
    if checkpoint:
        source = os.path.join(checkpoint, base)
        lines.append(f"  2. move the converted store aside and put the checkpoint in its place:")
        lines.append(f"       sudo mv {db} {db}.format-{store}-aside")
        lines.append(f"       sudo cp -a {source} {db}")
        lines.append(f"     checkpoint_dir: {checkpoint}")
    else:
        lines.append("  2. move the converted store aside and put the checkpoint in its place.")
        lines.append(
            f"     checkpoint_dir: none recorded in {db}.migration-checkpoint. "
            "Do not pick the newest .migration-backups/ entry; read the storage_migration report."
        )
    lines.append(
        f"  3. write the restored store's format into {db}.storage-format "
        f"(the value `{pre_format_bin} --print-storage-format` prints)"
    )
    lines.append(f"  4. install {pre_format_bin} as {bin_path} (never {bin_path}.prev)")
    lines.append(
        f"     note: {pre_format_bin} is saved by scripts/deploy-cutover.sh at deploy time; "
        f"if it is absent, build the release that matches format {store} and install that binary"
    )
    lines.append(
        "  5. sudo systemctl start audiobook-organizer.service, then check the "
        "`storage format: stamp=` log line"
    )
    lines.append(f"  6. See {RUNBOOK}")
    lines.append("")
    lines.append(
        "A restore discards every write made since the cut-over; get the owner's approval first."
    )
    return lines


def decide(db, bin_path, prev_text, sidecar_text, checkpoint_text, ignore_format,
           current_text="", errs=None):
    """Return (exit_code, output_lines). errs maps prev/current/sidecar/checkpoint to stderr text."""
    errs = errs or {}
    checkpoint = (checkpoint_text or "").strip()

    prev = _int_or_none(prev_text)
    if prev is None:
        # Every build before --print-storage-format rejects the unknown flag,
        # and every one of them reads format 1.
        prev = 1

    store = _int_or_none(sidecar_text)
    if store is None:
        cause = f"cause: {_cause(errs.get('sidecar'), sidecar_text)}"
        if ignore_format:
            current, why = _current_format(current_text, errs.get("current"))
            if current is None:
                return 1, [
                    f"REFUSING: cannot read the store's storage format from {db}.storage-format "
                    f"({cause}), and ROLLBACK_IGNORE_FORMAT=1 needs the current binary's format to "
                    f"vouch for the store, but `{bin_path} --print-storage-format` gave no answer "
                    f"({why}). Nothing was swapped.",
                    f"See {RUNBOOK}",
                ]
            if current > prev:
                return 1, [
                    f"REFUSING: cannot read the store's storage format from {db}.storage-format "
                    f"({cause}), and the current binary {bin_path} supports storage format {current}, "
                    f"above the {prev} the previous binary supports. ROLLBACK_IGNORE_FORMAT=1 cannot "
                    "vouch for a store a newer binary may already have converted; nothing was swapped.",
                    f"See {RUNBOOK}",
                ]
            return 0, [
                f"WARNING: cannot read the store's storage format from {db}.storage-format ({cause}); "
                "ROLLBACK_IGNORE_FORMAT=1 is set and the current binary supports no format above the "
                f"previous binary's ({current} <= {prev}; {why}; current stderr: "
                f"{_cause(errs.get('current'))}), so the rollback proceeds as if the store were at format 1."
            ]
        return 1, [
            f"REFUSING: cannot read the store's storage format from {db}.storage-format. "
            "Set ROLLBACK_IGNORE_FORMAT=1 only if you have verified the store is at format 1.",
            cause,
            "If the cause is sudo asking for a password, add the sudoers lines in "
            "docs/system/deploy-and-gpu-ops.md (Rollback flow).",
        ]

    if store > prev:
        lines = [
            f"REFUSING: the previous binary supports storage format {prev}, the store is at "
            f"{store}. A binary swap cannot go back past a format change; nothing was swapped."
        ]
        if _int_or_none(prev_text) is None:
            lines.append(f"(previous binary's --print-storage-format gave no format; read as 1. "
                         f"stderr: {_cause(errs.get('prev'))})")
        if ignore_format:
            lines.append("ROLLBACK_IGNORE_FORMAT=1 does not override a format change.")
        if not checkpoint and (errs.get("checkpoint") or "").strip():
            lines.append(f"reading {db}.migration-checkpoint failed: {_cause(errs.get('checkpoint'))}")
        lines.extend(restore_steps(db, bin_path, store, checkpoint))
        return 1, lines

    return 0, [f"storage format guard: store format {store}, previous binary supports {prev}; swap allowed."]


def main(argv=None):
    parser = argparse.ArgumentParser(description=__doc__.splitlines()[0])
    parser.add_argument("--db", required=True, help="main Pebble store path on the deploy host")
    parser.add_argument("--bin", required=True, help="deployed binary path on the deploy host")
    parser.add_argument("--prev", default="", help="stdout of <bin>.prev --print-storage-format")
    parser.add_argument("--sidecar", default="", help="content of <db>.storage-format")
    parser.add_argument("--checkpoint", default="", help="content of <db>.migration-checkpoint")
    parser.add_argument("--current", default="", help="stdout of <bin> --print-storage-format")
    for name in ("prev", "current", "sidecar", "checkpoint"):
        parser.add_argument(f"--{name}-err", default="", help=f"stderr of the ssh command behind --{name}")
    args = parser.parse_args(argv)

    ignore = os.environ.get("ROLLBACK_IGNORE_FORMAT", "").strip() == "1"
    errs = {
        "prev": args.prev_err,
        "current": args.current_err,
        "sidecar": args.sidecar_err,
        "checkpoint": args.checkpoint_err,
    }
    code, lines = decide(args.db.strip(), args.bin.strip(), args.prev, args.sidecar, args.checkpoint,
                         ignore, current_text=args.current, errs=errs)
    stream = sys.stdout if code == 0 else sys.stderr
    for line in lines:
        print(line, file=stream)
    return code


if __name__ == "__main__":
    sys.exit(main())
