#!/usr/bin/env python3
# file: scripts/r2corpus/overlay/build_overlay.py
# version: 1.0.0
# guid: c9c6a0fe-e62d-4811-a358-f201bb02e188
# last-edited: 2026-11-10
"""Build R2's fixture corpus, stage 2: the labelled overlay and the blocker corpus (F2-13).

09 §9.3 item 1: after stage 1 (F2-12, written by the R0 binary through its API),
"a second, separate and labelled stage then adds a hand-built overlay for the
cases R0's API cannot write, each case listed in the corpus' manifest", written
by "a program built at the R0 tag against HEAD's own internal/database", and an
identity row M3 cannot convert and an unfinished merge journal "in their own
corpus, which the preflight must refuse". This script:

1. reads the R0 tag (``ci/r0_release.txt``; the manifest's ``r0_tag`` must match),
2. checks that tag out in a temporary git worktree, copies the Go program in this
   directory into it and builds it there with ``-tags r2corpus_overlay``
   (main no longer has the tag's ``internal/database``, R1-26),
3. copies ``stage1/`` to ``overlay/`` and runs the program on the copy, then
   copies ``stage1/`` once per blocker store to ``blockers/<store>/`` and runs the
   program's blocker case there; stage 1's committed bytes are never touched,
4. checks the program wrote exactly the cases of ``CASES`` and that every key it
   recorded is in its store, scans every new store for leaks
   (``write_r0_corpus.scan_for_leaks``),
5. records each case in ``manifest.json`` (``overlay``, ``blockers``) and each
   store's files in ``stores``,
6. boots the R0 release binary on a copy of the overlay through
   ``record_r0_answers.py`` and writes its answers to ``answers/overlay/``,
   listed under the manifest's ``overlay_answers`` (with ``changed``: whether the
   answer differs from stage 1's).

Nothing is written without ``--regenerate``. The program's build needs Go and
git; the answers need the linux/amd64 R0 release asset (a linux/amd64 host, or
``--docker``), exactly as F2-12's recorder does. ``--no-answers`` skips step 6
(development; the manifest then keeps the overlay answers it had).
"""

from __future__ import annotations

import argparse
import dataclasses
import importlib.util
import json
import os
import pathlib
import shutil
import subprocess
import sys
import tempfile
from typing import Any

_HERE = pathlib.Path(__file__).resolve().parent
_R2 = _HERE.parent


def _load(name: str, path: pathlib.Path):
    """Import a sibling script by path (registered before exec, for dataclasses)."""
    if name in sys.modules:
        return sys.modules[name]
    spec = importlib.util.spec_from_file_location(name, path)
    assert spec and spec.loader
    mod = importlib.util.module_from_spec(spec)
    sys.modules[name] = mod
    spec.loader.exec_module(mod)
    return mod


w = _load("write_r0_corpus", _R2 / "write_r0_corpus.py")

BUILD_TAG = "r2corpus_overlay"
PROGRAM_REL = "scripts/r2corpus/overlay"
STAGE1_DIR = w.CORPUS_DIR / "stage1"
OVERLAY_REL = f"{w.CORPUS_REL}/overlay"
BLOCKERS_REL = f"{w.CORPUS_REL}/blockers"
OVERLAY_DIR = w.REPO_ROOT / OVERLAY_REL
BLOCKERS_DIR = w.REPO_ROOT / BLOCKERS_REL
OVERLAY_ANSWERS_DIR = w.ANSWERS_DIR / "overlay"

# The recorder's settle wait on the overlay (seconds). run-queued-deps stays
# queued until R0's dependency sweep, a fixed 5 minutes after boot at the tag
# (registry.go, no config surface), dispatches it; 300 s, the recorder's
# default for stage 1, races that tick.
SETTLE_TIMEOUT = 900

# Expected cutover outcomes (manifest field "outcome").
QUARANTINED = (
    "quarantined"  # copied under c/storage/quarantine/ with its reason; the step completes
)
IMPORTED = (
    "imported"  # converted by its step (with "reported": true, also named in the report and health)
)
REFUSED = "refused"  # the cutover preflight refuses the boot (exit 78), nothing written
NOT_BLOCKING = "not_blocking"  # read by the preflight and let through; imported by its step
OUTCOMES = (QUARANTINED, IMPORTED, REFUSED, NOT_BLOCKING)


@dataclasses.dataclass(frozen=True)
class Case:
    """One labelled case of stage 2 (manifest entry, F2-12's format plus F2-13's fields)."""

    label: str
    group: str  # "overlay" | "blockers"
    step: str  # M2 .. M6: the cutover step that reads it
    family: str
    key_prefix: str  # the primary family of the case's keys
    outcome: str
    expected: str
    spec: str
    reported: bool = False
    store: str = ""  # blockers: the directory under blockers/ (default: the label)

    def store_rel(self) -> str:
        if self.group == "overlay":
            return OVERLAY_REL
        return f"{BLOCKERS_REL}/{self.store or self.label}"


def _o(label, step, family, key_prefix, outcome, expected, spec, reported=False) -> Case:
    return Case(label, "overlay", step, family, key_prefix, outcome, expected, spec, reported)


def _b(label, family, key_prefix, outcome, expected, spec, store="") -> Case:
    return Case(label, "blockers", "M3", family, key_prefix, outcome, expected, spec, store=store)


_Q_STORAGE = "05-storage §5S.3.13 TestCutoverQuarantinesUndecodableRows"
_RBAC = "05-rbac §6.3; §12 TestIdentityCutover_RolesAndKeyScopes"
_AUTHN = "05-authn §11 (statuses: SEC-11); 09 §9.2.3 M3"
_TASKS = "05-tasks §5T.8.3; §5T.9 TestCutover_TasksFixtureFromR0"
_BLOCK = "05-storage §5S.3.13 TestCutoverStopsOnUnconvertibleIdentity"
_MERGE = "05-storage §5S.3.13 TestCutoverRefusedWhileMergeInProgress; 05-authn §11"
_REFUSE = (
    "the preflight refuses the boot (storage.ErrCutoverBlocked, exit 78); the report names the key "
    "and its reason, never its value; store and directory byte-identical, no checkpoint"
)

CASES: tuple[Case, ...] = (
    # ── M2: settings and secrets ──
    _o("corrupt-settings-secret", "M2", "setting: (encrypted secret row)", "setting:", QUARANTINED,
       "a setting record whose ciphertext HEAD's key does not decrypt: the ciphertext is quarantined "
       "sealed with the keyring, an unreadable marker is written under the secret's name, health raises "
       "secrets_undecryptable and Settings shows it must be re-entered; the step completes",
       "05-config §1.12.2 (HEAD's key); " + _Q_STORAGE),
    _o("blob-field-unmapped", "M2", "setting:config_blob (field)", "setting:config_blob", QUARANTINED,
       "a settings-blob field no rename-table row names: moved into a quarantine record (sealed), "
       "reported in health, the key keeping its default; the step completes",
       "05-config §1.12.2 (M2a)"),
    # ── M3: identity and roles ──
    _o("role-every-atom", "M3", "role:", "role:", IMPORTED,
       "a custom role holding every atom: imported as custom.<slug of its ID> with the union of the 13 "
       "rows' native scopes plus the ambient scopes, never core:admin; its holder james.gordon's role "
       "list is rewritten to the new name", _RBAC),
    _o("role-subset", "M3", "role:", "role:", IMPORTED,
       "a custom role holding a subset of the atoms: imported as custom.<slug> with the union of its "
       "atoms' native scopes plus the ambient scopes", _RBAC),
    _o("role-unknown-atom", "M3", "role:", "role:", IMPORTED,
       "a custom role naming one atom outside 05-rbac §6.1's table (batcave.access): imported, the "
       "unknown atom grants nothing, and the role is named in the dry-run report and in health",
       "05-rbac §6.3 (SEC4-13); §12 TestUnknownAtom_ReportedNotGranted", reported=True),
    _o("role-all-unknown", "M3", "role:", "role:", QUARANTINED,
       "a custom role whose every atom is unknown: quarantined together with the role-list entries "
       "that name it (talia.al.ghul keeps viewer; ubu's role list ends empty, as _system's is)",
       "05-rbac §6.3"),
    _o("corrupt-role", "M3", "role:", "role:", QUARANTINED,
       "a role row that does not decode: quarantined (a role is never a blocker); the step completes",
       "05-authn §11; " + _Q_STORAGE),
    _o("corrupt-inviteallow", "M3", "inviteallow:", "inviteallow:", QUARANTINED,
       "an invite allow-list row that does not decode: the preflight does not read the allow-list, "
       "and M3 quarantines the row; the step completes", "09 §9.2.3 (preflight); " + _Q_STORAGE),
    _o("key-outside-owner", "M3", "apikey:", "apikey:", IMPORTED,
       "an API key whose atoms (settings.manage, users.manage) lie outside its owner's roles: imported "
       "with the intersection, which is empty, so it keeps only the ambient scopes", _RBAC),
    _o("key-every-atom-custom-owner", "M3", "apikey:", "apikey:", IMPORTED,
       "an every-atom key whose owner holds a custom every-atom role, not HEAD's admin: imported with "
       "the 13 rows' union ∩ the owner's role set plus the ambient scopes, never core:admin", _RBAC),
    _o("key-empty-custom-owner", "M3", "apikey:", "apikey:", IMPORTED,
       "an empty scope list (HEAD's 'all of the owner's role permissions') under a custom-role owner: "
       "imported with the owner's native role set plus the ambient scopes, never empty", _RBAC),
    _o("status-edge-user-padded-active", "M3", "u:", "u:", IMPORTED,
       "user status ' Active': HEAD's UserIsActive trims and folds it, so it imports active", _AUTHN),
    _o("status-edge-user-upper-disabled", "M3", "u:", "u:", IMPORTED,
       "user status 'DISABLED': not active, so it imports disabled and is reported by name (it is not "
       "'inactive')", _AUTHN, reported=True),
    _o("status-edge-user-empty", "M3", "u:", "u:", IMPORTED,
       "user status '': an empty user status is active at HEAD, so it imports active", _AUTHN),
    _o("status-edge-user-padded-inactive", "M3", "u:", "u:", IMPORTED,
       "user status 'inactive ': trimmed to inactive, so it imports disabled and is not reported", _AUTHN),
    _o("status-edge-user-unknown", "M3", "u:", "u:", IMPORTED,
       "user status 'suspended': not active, so it imports disabled and is reported by name", _AUTHN,
       reported=True),
    _o("status-edge-key-padded-active", "M3", "apikey:", "apikey:", IMPORTED,
       "key status ' Active': trimmed and lower-cased, imports active", _AUTHN),
    _o("status-edge-key-upper-inactive", "M3", "apikey:", "apikey:", IMPORTED,
       "key status 'INACTIVE': lower-cased to inactive, imports disabled", _AUTHN),
    _o("status-edge-key-empty", "M3", "apikey:", "apikey:", IMPORTED,
       "key status '': HEAD admits it, imports active", _AUTHN),
    _o("status-edge-key-padded-revoked", "M3", "apikey:", "apikey:", IMPORTED,
       "key status 'Revoked ': trimmed and lower-cased, imports revoked", _AUTHN),
    _o("status-edge-key-active-revoked-at", "M3", "apikey:", "apikey:", IMPORTED,
       "key status 'active' with revoked_at set: imports revoked (early fix 3's rule)", _AUTHN),
    _o("status-edge-key-unknown", "M3", "apikey:", "apikey:", IMPORTED,
       "key status 'paused': not a known status, imports disabled and is reported by key ID", _AUTHN,
       reported=True),
    _o("status-edge-key-no-expiry", "M3", "apikey:", "apikey:", IMPORTED,
       "an active key with no expiry: imports active with HEAD's one-shot stamp (cutover time plus the "
       "default TTL)", _AUTHN),
    _o("oauthid-linked", "M3", "oauthid:", "oauthid:", IMPORTED,
       "an OAuth identity linked to barbara.gordon (R0's API writes one only through an SSO sign-in): "
       "imported to c/auth/identity/ with (provider, subject) and user kept", "05-authn §11"),
    _o("inviteallow-accepted", "M3", "inviteallow:", "inviteallow:", IMPORTED,
       "the allow-list row an emailed invite's acceptance writes (with its user alfred.pennyworth): "
       "imported to c/auth/inviteallow/ with the folded e-mail kept", "05-authn §11"),
    # ── M4: tasks ──
    _o("run-running", "M4", "opv3:run: (running)", "opv3:run:", IMPORTED,
       "a running run with a checkpoint, a chunk ledger with marks and a lease, an open intent, an item "
       "outcome and log lines: imported as interrupted with all of them", _TASKS),
    _o("run-stopping-shutdown", "M4", "opv3:run: (stopping)", "opv3:run:", IMPORTED,
       "stopping with StopReason shutdown: imported as interrupted", _TASKS),
    _o("run-stopping-user", "M4", "opv3:run: (stopping)", "opv3:run:", IMPORTED,
       "stopping with StopReason user: imported as canceled", _TASKS),
    _o("run-stopping-quiesce", "M4", "opv3:run: (stopping)", "opv3:run:", IMPORTED,
       "stopping with StopReason quiesce (a library.scan): imported as interrupted_quiesced", _TASKS),
    _o("run-stopping-timeout", "M4", "opv3:run: (stopping)", "opv3:run:", IMPORTED,
       "stopping with StopReason timeout: imported as timed_out", _TASKS),
    _o("run-stopping-watchdog", "M4", "opv3:run: (stopping)", "opv3:run:", IMPORTED,
       "stopping with StopReason watchdog: imported as failed with reason watchdog", _TASKS),
    _o("run-interrupted-crash", "M4", "opv3:run: (interrupted)", "opv3:run:", IMPORTED,
       "interrupted with InterruptedWhy crash: imported as interrupted", _TASKS),
    _o("run-interrupted-quiesced", "M4", "opv3:run: (interrupted)", "opv3:run:", IMPORTED,
       "interrupted with InterruptedWhy quiesced (a library.scan): imported as interrupted_quiesced",
       _TASKS),
    _o("run-queued-deps", "M4", "opv3:run: (queued, deps)", "opv3:run:", IMPORTED,
       "queued with WaitReason deps (dedup.check-book on a book): imported as waiting_deps", _TASKS),
    _o("run-dropped", "M4", "opv3:run: (dropped)", "opv3:run:", IMPORTED,
       "dropped by the owner after a crash: imported as interrupted_dropped", _TASKS),
    _o("run-awaiting-decision", "M4", "opv3:run: + opv2:row:", "opv3:run:", IMPORTED,
       "a repairs plan awaiting its approver, with its two plan rows (opv2:row: and opv2:rowix:, "
       "written by HEAD's repairs.SavePlan): imported as awaiting_decision with its rows under "
       "c/tasks/row/", _TASKS + " (DATA-3)"),
    _o("run-interrupted-apply-rowmarks", "M4", "opv3:run: + opv3:rowmark:", "opv3:run:", IMPORTED,
       "an interrupted live repairs.apply with one row's apply marker (opv3:rowmark:): imported with the "
       "marker under c/tasks/mark/, so a resume does not apply that row again", _TASKS + " (DATA-3)"),
    _o("run-standdown-marker", "M4", "setting:registry.scan_standdown", "setting:registry.scan_standdown",
       IMPORTED,
       "a scan stand-down marker naming a quiesced library.scan and the interrupted maintenance op "
       "holding the gate (its lease already expired when written): imported as one StandDown for the "
       "converted scan kind", _TASKS),
    _o("run-debounce-bucket", "M4", "op:batch:", "op:batch:", IMPORTED,
       "one pending subject in dedup.check-book's debounce bucket: imported as a Debounce holding a "
       "pending start", _TASKS),
    _o("run-requires-stamps", "M4", "op:deprev:, op:completion:", "op:", IMPORTED,
       "a book's dependency revision and its dedup.check-book completion stamp: imported as a Stamp "
       "with subject books/books and the renamed kind", _TASKS),
    _o("corrupt-run", "M4", "opv3:run:", "opv3:run:", QUARANTINED,
       "a finished run whose record does not decode while its progress row and opv2 mirror row stay "
       "intact and current (so HEAD's ReconcileOpsV3WithV2 neither rebuilds nor deletes it): "
       "quarantined and reported; the step completes",
       "05-tasks §5T.9 TestCutover_UndecodableRowQuarantined; " + _Q_STORAGE),
    # ── M5: system log ──
    _o("corrupt-syslog", "M5", "syslog:", "syslog:", QUARANTINED,
       "a syslog: row that does not decode: quarantined; the step completes",
       "05-logging TestCutover_SyslogImported; " + _Q_STORAGE),
    # ── M6: integrations, import roots, AI jobs ──
    _o("integration-field-invalid", "M6", "setting:config_blob (ai_endpoints row)", "setting:config_blob",
       IMPORTED,
       "an AI endpoint row (oracle-relay) whose URL does not parse: M6 writes the native ai/oracle-relay "
       "instance, keeps it and reports it misconfigured (not quarantined: no integration field can be "
       "undecodable while R0 still boots, see seams.d/F2-13.md)", "05-metadata M1.10; 05-ai §10",
       reported=True),
    _o("corrupt-import-root", "M6", "import_path:", "import_path:", QUARANTINED,
       "an import_path: row that does not decode, whose path (import/Selina Kyle) prefixes the Hush book's "
       "rows: quarantined; those books rows stay byte-identical and are named in the report and health; "
       "the step completes",
       "05-storage §5S.3.13 TestCutoverQuarantinedParentKeepsReferencingRows; " + _Q_STORAGE),
    _o("corrupt-aijob", "M6", "aijob:", "aijob:", QUARANTINED,
       "an aijob: row that does not decode: quarantined; the step completes", "05-ai §10; " + _Q_STORAGE),
    _o("aijob-pending", "M6", "aijob:", "aijob:", IMPORTED,
       "an AI job pending (R0's API makes one only through the OpenAI Batch API): imported into c/ai/",
       "05-ai §10"),
    _o("aijob-submitted", "M6", "aijob:", "aijob:", IMPORTED,
       "an AI job submitted with its batch index row: imported into c/ai/", "05-ai §10"),
    _o("aijob-completed-with-errors", "M6", "aijob:", "aijob:", IMPORTED,
       "an AI job completed with one row error: imported into c/ai/", "05-ai §10"),
    _o("aijob-failed", "M6", "aijob:", "aijob:", IMPORTED,
       "an AI job failed: imported into c/ai/", "05-ai §10"),
    # ── blockers: each in its own copy of stage 1 ──
    _b("blocker-identity-corrupt-user", "u:", "u:", REFUSED,
       "a u: row that does not decode: " + _REFUSE, _BLOCK),
    _b("blocker-identity-corrupt-apikey", "apikey:", "apikey:", REFUSED,
       "an apikey: row that does not decode: " + _REFUSE, _BLOCK + " (the apikey: cell; F4-06)"),
    _b("blocker-identity-corrupt-oauthid", "oauthid:", "oauthid:", REFUSED,
       "an oauthid: row that does not decode: " + _REFUSE, _BLOCK + " (the oauthid: cell)"),
    _b("blocker-identity-user-rejected", "u:", "u:", REFUSED,
       "a decodable user row auth.Importer rejects (an empty username): " + _REFUSE, _BLOCK),
    _b("blocker-identity-apikey-orphan", "apikey:", "apikey:", REFUSED,
       "a decodable API key whose user does not exist: " + _REFUSE, _BLOCK),
    _b("blocker-identity-oauthid-orphan", "oauthid:", "oauthid:", REFUSED,
       "a decodable OAuth identity whose user does not exist: " + _REFUSE, _BLOCK),
    _b("blocker-merge-applying", "usermerge:journal:", "usermerge:journal:", REFUSED,
       "a merge journal at applying (HEAD's merge canceled mid-apply): the preflight refuses the boot "
       "naming the merge and the fix (finish or undo it on the pre-freeze release), nothing written",
       _MERGE),
    _b("blocker-merge-undoing", "usermerge:journal:", "usermerge:journal:", REFUSED,
       "a merge journal at undoing (HEAD's undo started and canceled): refused as above", _MERGE),
    _b("blocker-merge-applied", "usermerge:journal:", "usermerge:journal:", NOT_BLOCKING,
       "a finished merge (applied, the absorbed account retired): does not block; its merge is "
       "imported, M3 rewrites the journal once with its invite tokens hashed, and an undo after R2 "
       "answers 409 merge_permanent. Shares its store with blocker-merge-undone: both are controls",
       _MERGE, store="blocker-merge-terminal"),
    _b("blocker-merge-undone", "usermerge:journal:", "usermerge:journal:", NOT_BLOCKING,
       "a merge applied and then undone in full: does not block (terminal), kept read-only. Shares "
       "its store with blocker-merge-applied", _MERGE, store="blocker-merge-terminal"),
)  # fmt: skip


def overlay_cases() -> tuple[Case, ...]:
    return tuple(c for c in CASES if c.group == "overlay")


def blocker_cases() -> tuple[Case, ...]:
    return tuple(c for c in CASES if c.group == "blockers")


def blocker_stores() -> list[str]:
    """Each blocker store once, in case order."""
    out: list[str] = []
    for c in blocker_cases():
        name = c.store or c.label
        if name not in out:
            out.append(name)
    return out


def manifest_entry(c: Case, keys: list[str]) -> dict[str, Any]:
    return {
        "case": c.label,
        "label": c.label,
        "family": c.family,
        "step": c.step,
        "key_prefix": c.key_prefix,
        "keys": keys,
        "outcome": c.outcome,
        "reported": c.reported,
        "expected": c.expected,
        "spec": c.spec,
        "store": c.store_rel(),
    }


# ── building and running the program at the tag ────────────────────────────


def run(cmd: list[str], cwd: pathlib.Path | None = None, env: dict[str, str] | None = None) -> str:
    p = subprocess.run(cmd, cwd=cwd, env=env, capture_output=True, text=True)
    if p.returncode != 0:
        raise w.CorpusError(f"{' '.join(cmd)} -> {p.returncode}\n{p.stdout}\n{p.stderr}")
    return p.stdout


def build_program(tag: str, scratch: pathlib.Path) -> pathlib.Path:
    """Build the overlay program in a worktree at tag; returns the binary."""
    tree = scratch / "r0-tag"
    run(["git", "-C", str(w.REPO_ROOT), "worktree", "add", "--detach", str(tree), tag])
    try:
        dest = tree / PROGRAM_REL
        dest.mkdir(parents=True, exist_ok=True)
        for src in sorted(_HERE.glob("*.go")):
            shutil.copy2(src, dest / src.name)
        binary = scratch / "overlay"
        run(["go", "build", "-tags", BUILD_TAG, "-o", str(binary), f"./{PROGRAM_REL}"], cwd=tree)
        return binary
    finally:
        run(["git", "-C", str(w.REPO_ROOT), "worktree", "remove", "--force", str(tree)])


def program_cases(binary: pathlib.Path) -> tuple[list[str], list[str]]:
    overlay, blockers = [], []
    for line in run([str(binary), "-list"]).splitlines():
        group, label = line.split(" ", 1)
        (overlay if group == "overlay" else blockers).append(label)
    return overlay, blockers


def write_store(
    binary: pathlib.Path, dest: pathlib.Path, args: list[str], report: pathlib.Path
) -> dict[str, list[str]]:
    """Copy stage 1 to dest, run the program on it, return {case: keys}."""
    if dest.exists():
        shutil.rmtree(dest)
    dest.parent.mkdir(parents=True, exist_ok=True)
    shutil.copytree(STAGE1_DIR, dest)
    run([str(binary), "-store", str(dest), *args, "-report", str(report)])
    out = {}
    for r in json.loads(report.read_text(encoding="utf-8")):
        if not r["keys"]:
            raise w.CorpusError(f"case {r['case']} wrote no key")
        out[r["case"]] = r["keys"]
    return out


def check_written(store: str, want: list[str], got: dict[str, list[str]]) -> None:
    if list(got) != want:
        raise w.CorpusError(f"{store}: the program wrote {list(got)}, CASES says {want}")


def bump_patch(version: str) -> str:
    major, minor, patch = (int(x) for x in version.split("."))
    return f"{major}.{minor}.{patch + 1}"


def changed_answers(stage1_dir: pathlib.Path, overlay_dir: pathlib.Path) -> dict[str, bool]:
    """Which overlay answers differ from stage 1's (exchanges compared, normalized by the recorder)."""
    out = {}
    for p in sorted(overlay_dir.glob("*.json")):
        mine = json.loads(p.read_text(encoding="utf-8")).get("exchanges")
        base = stage1_dir / p.name
        theirs = (
            json.loads(base.read_text(encoding="utf-8")).get("exchanges") if base.exists() else None
        )
        out[p.stem] = mine != theirs
    return out


def record_answers(args: argparse.Namespace, answers_dir: pathlib.Path) -> None:
    cmd = [sys.executable, str(_R2 / "record_r0_answers.py"), "--regenerate",
           "--corpus", str(OVERLAY_DIR), "--out", str(answers_dir),
           "--settle-timeout", str(SETTLE_TIMEOUT)]  # fmt: skip
    for flag in ("binary", "tag", "cache", "workdir"):
        v = getattr(args, flag)
        if v:
            cmd += [f"--{flag}", str(v)]
    if args.port:
        cmd += ["--port", str(args.port)]
    if args.docker:
        cmd.append("--docker")
    if args.allow_foreign_binary:
        cmd.append("--allow-foreign-binary")
    p = subprocess.run(cmd)
    if p.returncode != 0:
        raise w.CorpusError(f"record_r0_answers.py on the overlay -> {p.returncode}")


def build(args: argparse.Namespace) -> int:
    tag = args.tag or w.read_r0_tag()
    manifest = json.loads(w.MANIFEST_PATH.read_text(encoding="utf-8"))
    if manifest["r0_tag"] != tag:
        raise w.CorpusError(f"manifest r0_tag {manifest['r0_tag']} != {tag}")
    answers_dir = (
        pathlib.Path(args.answers_out).resolve() if args.answers_out else OVERLAY_ANSWERS_DIR
    )
    if args.allow_foreign_binary and answers_dir == OVERLAY_ANSWERS_DIR and not args.no_answers:
        raise w.CorpusError(
            "--allow-foreign-binary is for development: give --answers-out or --no-answers"
        )

    with tempfile.TemporaryDirectory(prefix="r2corpus-overlay-") as tmp:
        scratch = pathlib.Path(tmp)
        binary = build_program(tag, scratch)
        prog_overlay, prog_blockers = program_cases(binary)
        if prog_overlay != [c.label for c in overlay_cases()]:
            raise w.CorpusError(f"the program's overlay cases {prog_overlay} are not CASES'")
        if prog_blockers != [c.label for c in blocker_cases()]:
            raise w.CorpusError(f"the program's blocker cases {prog_blockers} are not CASES'")

        written: dict[str, list[str]] = {}
        got = write_store(binary, OVERLAY_DIR, ["-corpus", "overlay"], scratch / "overlay.json")
        check_written(OVERLAY_REL, [c.label for c in overlay_cases()], got)
        written.update(got)
        if BLOCKERS_DIR.exists():
            shutil.rmtree(BLOCKERS_DIR)
        for store in blocker_stores():
            want = [c.label for c in blocker_cases() if (c.store or c.label) == store]
            got = write_store(binary, BLOCKERS_DIR / store, ["-corpus", "blocker", "-case", store],
                              scratch / f"{store}.json")  # fmt: skip
            check_written(f"{BLOCKERS_REL}/{store}", want, got)
            written.update(got)

    stores = {OVERLAY_REL: OVERLAY_DIR}
    stores.update({f"{BLOCKERS_REL}/{s}": BLOCKERS_DIR / s for s in blocker_stores()})
    findings = [f"{rel}: {f}" for rel, d in stores.items() for f in w.scan_for_leaks(d)]
    if findings:
        raise w.CorpusError("leak scan failed:\n  " + "\n  ".join(findings))

    manifest["overlay"] = [manifest_entry(c, written[c.label]) for c in overlay_cases()]
    manifest["blockers"] = [manifest_entry(c, written[c.label]) for c in blocker_cases()]
    manifest["stores"] = {
        k: v for k, v in manifest["stores"].items()
        if not (k == OVERLAY_REL or k.startswith(BLOCKERS_REL + "/"))
    }  # fmt: skip
    for rel, d in stores.items():
        manifest["stores"][rel] = w.store_files(d)
    manifest["stage2"] = {
        "program": PROGRAM_REL,
        "build_tag": BUILD_TAG,
        "built_at": tag,
        "applied_to": "a copy of stage1/ per store; stage 1's bytes are unchanged",
        "outcomes": {
            QUARANTINED: "copied under c/storage/quarantine/ with its reason, counted in the step's report "
            "and in health; the step completes. The overlay's quarantine report is exactly these cases",
            IMPORTED: "converted by its step; reported: true means it is also named in the step's report "
            "and in health",
            REFUSED: "the cutover preflight refuses the boot (storage.ErrCutoverBlocked, exit 78) and "
            "writes nothing",
            NOT_BLOCKING: "read by the preflight and let through; converted by its step",
        },
        "keys": "every key the case wrote (primary rows and the indexes the writer kept with them); "
        "setting:config_blob#<field> names one field of the settings blob",
    }  # fmt: skip

    if not args.no_answers:
        record_answers(args, answers_dir)
        if answers_dir == OVERLAY_ANSWERS_DIR:
            changed = changed_answers(w.ANSWERS_DIR, answers_dir)
            rec = _load("record_r0_answers", _R2 / "record_r0_answers.py")
            what = dict(rec.ANSWERS)
            manifest["overlay_answers"] = [
                {
                    "name": name,
                    "file": (answers_dir / f"{name}.json").relative_to(w.REPO_ROOT).as_posix(),
                    "what": what.get(name, ""),
                    "changed": changed[name],
                }
                for name in changed
            ]
    header = dict(manifest["_header"])
    header["version"] = bump_patch(header["version"])
    header["last-edited"] = _today()
    manifest["_header"] = header
    w.write_manifest(manifest, w.MANIFEST_PATH)
    print(f"wrote {len(manifest['overlay'])} overlay and {len(manifest['blockers'])} blocker cases")
    return 0


def _today() -> str:
    import datetime as _dt

    return _dt.datetime.now(_dt.timezone.utc).strftime("%Y-%m-%d")


def build_parser() -> argparse.ArgumentParser:
    ap = argparse.ArgumentParser(
        description="Build R2's stage-2 corpus: the labelled overlay and the blocker corpus (F2-13).",
    )
    ap.add_argument(
        "--regenerate",
        action="store_true",
        help="build the program at the R0 tag and (re)write overlay/, blockers/ and the manifest",
    )
    ap.add_argument("--list", action="store_true", help="list every case with its expected outcome")
    ap.add_argument(
        "--no-answers", action="store_true", help="skip recording R0's answers on the overlay"
    )
    ap.add_argument(
        "--answers-out", help="write the overlay answers here instead of answers/overlay/"
    )
    ap.add_argument("--tag", help="override ci/r0_release.txt (a re-cut only)")
    ap.add_argument(
        "--binary", help="the R0 binary the recorder boots (default: the release asset)"
    )
    ap.add_argument("--cache", help="the recorder's download cache")
    ap.add_argument(
        "--docker", action="store_true", help="the recorder runs R0 in a linux/amd64 container"
    )
    ap.add_argument("--workdir", help="the recorder's fixed work directory")
    ap.add_argument("--port", type=int, help="the recorder's port")
    ap.add_argument(
        "--allow-foreign-binary",
        action="store_true",
        help="development only: a non-linux/amd64 R0 build; needs --answers-out or --no-answers",
    )
    return ap  # fmt: skip


def main(argv: list[str] | None = None) -> int:
    args = build_parser().parse_args(argv)
    if args.list:
        for c in CASES:
            extra = " (reported)" if c.reported else ""
            print(f"{c.group}\t{c.step}\t{c.label}\t{c.outcome}{extra}\t{c.store_rel()}")
        return 0
    if not args.regenerate:
        print("nothing to do: pass --list, or --regenerate to rebuild stage 2", file=sys.stderr)
        return 2
    os.environ.setdefault("GOFLAGS", "-mod=mod")
    try:
        return build(args)
    except w.CorpusError as e:
        print(f"ERROR: {e}", file=sys.stderr)
        return 1


if __name__ == "__main__":
    sys.exit(main())
