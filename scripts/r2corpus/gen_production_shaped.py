#!/usr/bin/env python3
# file: scripts/r2corpus/gen_production_shaped.py
# version: 1.0.0
# guid: 8a6cf01e-e125-4f5e-b9bc-bb354a2a73ee
# last-edited: 2026-11-10
"""Generate R2's production-shaped store from F2-12's stage-1 corpus (F2-14).

09 §9.3 item 1: the third leg of the R2 corpus is a store grown to the owner's
recorded P1 counts and sizes (09 §9.7 P1: keys, raw key bytes and raw value bytes
per key family), which the R2 steps run over on ``unit-lifecycle``'s triggers and
nightly for the per-family checks and the boot-time measurement. The store is
never committed: the job generates it each run.

This script:

1. checks stage 1 (and the F2-13 overlay store, a template source for families
   stage 1 has no row of) against ``manifest.json``'s ``stores`` entry: every
   file, size and sha256, and no file the manifest does not list. A mismatch
   exits non-zero, so a store the repository's ``.gitignore`` thinned out is
   caught here too;
2. copies stage 1 into ``--out``;
3. builds and runs the Go row writer (``scripts/r2corpus/shaped``, build tag
   ``r2corpus_shaped``), which grows each family to its P1 key count with keys and
   values of the P1 average sizes, cloning that family's stage-1 rows so every
   generated row decodes as R0 wrote it;
4. writes ``<out>/store_manifest.json``, every file of the generated store except
   itself as ``{"path", "size", "sha256"}`` (the format of ``manifest.json``'s
   ``stores`` lists), which the boot-time measurement test checks before it boots;
5. prints, per family, what was generated. A family with no template row anywhere
   is skipped and listed, never invented.

Stage-1 rows are never deleted: a family stage 1 already holds more of than the
P1 count (a handful of users, say) keeps them.

``--scale`` is for local runs; CI runs at scale 1.
"""

from __future__ import annotations

import argparse
import hashlib
import json
import os
import pathlib
import shutil
import subprocess
import sys
import tempfile

REPO_ROOT = pathlib.Path(__file__).resolve().parents[2]
CORPUS_REL = "internal/books/testdata/r2corpus"
CORPUS_DIR = REPO_ROOT / CORPUS_REL
STAGE1_KEY = f"{CORPUS_REL}/stage1"
OVERLAY_KEY = f"{CORPUS_REL}/overlay"
DEFAULT_COUNTS = CORPUS_DIR / "p1_counts.json"
DEFAULT_MANIFEST = CORPUS_DIR / "manifest.json"
SHAPED_PKG = "./scripts/r2corpus/shaped"
SHAPED_TAG = "r2corpus_shaped"
STORE_MANIFEST = "store_manifest.json"
PEBBLE_REL = "db/audiobooks.pebble"


class GenerateError(RuntimeError):
    """A precondition the generator cannot continue past (exit 1)."""


def sha256_file(path: pathlib.Path) -> str:
    h = hashlib.sha256()
    with path.open("rb") as fh:
        for chunk in iter(lambda: fh.read(1 << 20), b""):
            h.update(chunk)
    return h.hexdigest()


def store_files(root: pathlib.Path, exclude: frozenset[str] = frozenset()) -> list[dict]:
    """Every regular file under root as {"path", "size", "sha256"}, by path."""
    out = []
    for dirpath, _dirs, files in os.walk(root):
        for name in files:
            path = pathlib.Path(dirpath) / name
            rel = path.relative_to(root).as_posix()
            if rel in exclude:
                continue
            out.append({"path": rel, "size": path.stat().st_size, "sha256": sha256_file(path)})
    return sorted(out, key=lambda f: f["path"])


def check_store(root: pathlib.Path, entries: list[dict]) -> list[str]:
    """Problems between a directory and a manifest list: missing, extra, size, sha256."""
    if not root.is_dir():
        return [f"{root} is not a directory"]
    want = {e["path"]: e for e in entries}
    have = {f["path"]: f for f in store_files(root)}
    problems = []
    for path in sorted(want.keys() - have.keys()):
        problems.append(f"{path}: in the manifest but not on disk")
    for path in sorted(have.keys() - want.keys()):
        problems.append(f"{path}: on disk but not in the manifest")
    for path in sorted(want.keys() & have.keys()):
        if want[path]["size"] != have[path]["size"]:
            problems.append(f"{path}: size {have[path]['size']}, manifest says {want[path]['size']}")
        elif want[path]["sha256"] != have[path]["sha256"]:
            problems.append(f"{path}: sha256 differs from the manifest")
    return problems


def load_counts(path: pathlib.Path) -> list[dict]:
    """The families of a p1_counts.json, checked for shape (counts only)."""
    doc = json.loads(path.read_text(encoding="utf-8"))
    fams = doc.get("families")
    if not isinstance(fams, list) or not fams:
        raise GenerateError(f"{path}: no families")
    seen = set()
    for f in fams:
        if not isinstance(f, dict) or set(f) != {"prefix", "keys", "raw_key_bytes", "raw_value_bytes"}:
            raise GenerateError(f"{path}: a family row is not {{prefix, keys, raw_key_bytes, raw_value_bytes}}: {f!r}")
        if not f["prefix"] or f["prefix"] in seen:
            raise GenerateError(f"{path}: empty or repeated prefix {f['prefix']!r}")
        seen.add(f["prefix"])
        if any(not isinstance(f[k], int) or f[k] < 0 for k in ("keys", "raw_key_bytes", "raw_value_bytes")):
            raise GenerateError(f"{path}: {f['prefix']} has a count that is not a non-negative integer")
    return fams


def verified_store(manifest: dict, key: str, root: pathlib.Path, what: str) -> None:
    entries = manifest.get("stores", {}).get(key)
    if entries is None:
        raise GenerateError(f"manifest.json has no stores entry {key} for {what}")
    problems = check_store(root, entries)
    if problems:
        shown = "\n  ".join(problems[:20])
        more = f"\n  ... and {len(problems) - 20} more" if len(problems) > 20 else ""
        raise GenerateError(f"{what} {root} does not match manifest.json:\n  {shown}{more}")


def build_row_writer(go: str, dest: pathlib.Path) -> pathlib.Path:
    exe = dest / "shaped"
    subprocess.run([go, "build", "-tags", SHAPED_TAG, "-o", str(exe), SHAPED_PKG], cwd=REPO_ROOT, check=True)
    return exe


def print_report(report: dict, out=None) -> None:
    out = out or sys.stdout
    fams = report["families"]
    print(f"production-shaped store at scale {report['scale']}: {len(fams)} families", file=out)
    print(f"{'family':40} {'target':>12} {'stage1':>8} {'generated':>12} {'keys':>12}  template", file=out)
    for f in fams:
        src = f["template_source"] or "-"
        line = (
            f"{f['prefix']:40} {f['target_keys']:>12} {f['stage1_keys']:>8} "
            f"{f['generated_keys']:>12} {f['keys']:>12}  {src}"
        )
        if f.get("skipped"):
            line += f"  SKIPPED: {f['skipped']}"
        print(line, file=out)
        for note in f.get("notes") or []:
            print(f"{'':40}   note: {note}", file=out)
    skipped = [f["prefix"] for f in fams if f.get("skipped")]
    print(f"skipped families ({len(skipped)}): {', '.join(skipped) or 'none'}", file=out)
    print(f"stage-1 keys outside every family, left as they were: {report['other_keys']}", file=out)


def generate(args: argparse.Namespace) -> dict:
    counts = pathlib.Path(args.counts)
    load_counts(counts)
    manifest = json.loads(pathlib.Path(args.manifest).read_text(encoding="utf-8"))
    stage1 = pathlib.Path(args.stage1)
    out = pathlib.Path(args.out)
    if out.exists() and any(out.iterdir()):
        raise GenerateError(f"--out {out} exists and is not empty")

    verified_store(manifest, args.stage1_key, stage1, "stage 1")
    overlay = None
    if not args.no_overlay and (pathlib.Path(args.overlay) / PEBBLE_REL).is_dir():
        overlay = pathlib.Path(args.overlay)
        verified_store(manifest, args.overlay_key, overlay, "the overlay store")

    out.mkdir(parents=True, exist_ok=True)
    shutil.copytree(stage1, out, dirs_exist_ok=True)
    for path in out.rglob("*"):  # the Go program writes into the copy
        path.chmod(path.stat().st_mode | 0o200)

    with tempfile.TemporaryDirectory(prefix="shaped-") as tmp:
        tmp_dir = pathlib.Path(tmp)
        exe = build_row_writer(args.go, tmp_dir)
        cmd = [
            str(exe), "-mode", "generate",
            "-store", str(out / PEBBLE_REL),
            "-counts", str(counts),
            "-scale", repr(float(args.scale)),
            "-report", str(tmp_dir / "report.json"),
        ]
        if args.workers:
            cmd += ["-workers", str(args.workers)]
        if overlay is not None:
            # A copy: the program opens it read-only, but the committed store's
            # files are the manifest's and are never opened in place.
            tpl = tmp_dir / "overlay.pebble"
            shutil.copytree(overlay / PEBBLE_REL, tpl)
            cmd += ["-templates", str(tpl)]
        # Its stdout is the report (read from the file); its stderr is Pebble's
        # recovery log, shown only when it fails.
        proc = subprocess.run(cmd, cwd=REPO_ROOT, stdout=subprocess.DEVNULL, stderr=subprocess.PIPE, text=True)
        if proc.returncode != 0:
            raise GenerateError(f"the row writer exited {proc.returncode}:\n{proc.stderr[-4000:]}")
        report = json.loads((tmp_dir / "report.json").read_text(encoding="utf-8"))

    entries = store_files(out, exclude=frozenset({STORE_MANIFEST}))
    (out / STORE_MANIFEST).write_text(json.dumps(entries, indent=2) + "\n", encoding="utf-8")
    if args.report:
        pathlib.Path(args.report).write_text(json.dumps(report, indent=2) + "\n", encoding="utf-8")
    return report


def main(argv: list[str] | None = None) -> int:
    p = argparse.ArgumentParser(description=__doc__.split("\n\n")[0])
    p.add_argument("--counts", default=str(DEFAULT_COUNTS), help="p1_counts.json")
    p.add_argument("--out", required=True, help="directory to write the store to (empty or absent)")
    p.add_argument("--scale", type=float, default=1.0, help="fraction of the P1 counts (local runs; CI uses 1)")
    p.add_argument("--stage1", default=str(CORPUS_DIR / "stage1"), help="F2-12's stage-1 store")
    p.add_argument("--manifest", default=str(DEFAULT_MANIFEST), help="manifest.json holding the stores' file lists")
    p.add_argument("--stage1-key", default=STAGE1_KEY, help="the manifest's stores key of stage 1")
    p.add_argument("--overlay", default=str(CORPUS_DIR / "overlay"), help="F2-13's overlay store (template source)")
    p.add_argument("--overlay-key", default=OVERLAY_KEY, help="the manifest's stores key of the overlay store")
    p.add_argument("--no-overlay", action="store_true", help="take no templates from the overlay store")
    p.add_argument("--report", help="also write the generator's JSON report here")
    p.add_argument("--workers", type=int, default=0, help="concurrent batch writers (default: one per CPU)")
    p.add_argument("--go", default=os.environ.get("GO", "go"), help="the go command")
    args = p.parse_args(argv)
    if args.scale <= 0:
        p.error("--scale must be positive")
    try:
        report = generate(args)
    except GenerateError as e:
        print(f"gen_production_shaped: {e}", file=sys.stderr)
        return 1
    except subprocess.CalledProcessError as e:
        print(f"gen_production_shaped: {' '.join(map(str, e.cmd))} exited {e.returncode}", file=sys.stderr)
        return 1
    print_report(report)
    return 0


if __name__ == "__main__":
    sys.exit(main())
