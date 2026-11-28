#!/usr/bin/env python3
# file: scripts/deploy_env_check.py
# version: 0.1.0
# guid: c1b7e4a9-3d52-4f60-8a1e-9b2d6f0c4e73
# last-edited: 2026-11-19

"""`make deploy-env-check`: refuse a deploy that cannot be rolled back, or
that ships environment the new binary ignores.

Run before anything is uploaded or installed (by `make deploy` and `make
deploy-debug`, and by scripts/deploy-preflight.sh, which a Makefile.local that
has not been synced from Makefile.local.example still calls). It makes two
checks and reads no host:

retired
    deploy/local.conf may not set an env name the binary being deployed
    would not read. The names are deploy/retired-env-names.txt's, generated
    from the rename table (internal/books/cutover/retired_env_list_test.go
    fails when the file and the table differ), never listed here. A name with
    verdict `warn` is ignored with one warning at boot: the setting silently
    stops applying, which for a peer allowlist or an auth setting is a
    fail-open. A name with verdict `refuse` stops the boot. While the list
    says `enforced: false` (the app does not yet hand the table to core, so
    the binary still reads every one of these names) the names found are
    reported and the check passes; the marker is derived from the source, so
    the change that wires the table flips it.

makefile-local
    Makefile.local's `deploy` and `deploy-debug` recipes must go through the
    root-owned install-staged helper. A recipe that replaces the binary, the
    unit or local.conf with `sudo mv` or `sudo cp` keeps no copy of what it
    replaced, so `make rollback` has no previous unit or local.conf to put
    back, and no `.prev` binary either. `make rollback` and `make
    rollback-restore` refuse then; this refuses earlier, before the deploy
    that cannot be undone. Makefile.local.example's recipes are the model.
    DEPLOY_ALLOW_NO_ROLLBACK=1 deploys anyway, with a warning that says
    which release a rollback then goes back to, EXCEPT when the list says
    `enforced: true`: the build that carries the flip cannot be deployed
    without rollback state (the previous release reads names it retires), so
    the override is refused there.

Usage:
    python3 scripts/deploy_env_check.py [retired|makefile-local ...]
        [--local-conf deploy/local.conf] [--list deploy/retired-env-names.txt]
        [--unit-template deploy/audiobook-organizer.service]
        [--makefile-local Makefile.local]

With no check named, both run. Paths default to the current directory's
(the repository root, where make runs), except the list, which is the one
beside this script. A missing deploy/local.conf or Makefile.local is not a
problem: there is nothing to check. Exit 0 ok; 1 refused; 2 bad usage or a
malformed list. Standard library only. Tests:
scripts/tests/test_deploy_env_check.py.
"""

from __future__ import annotations

import argparse
import os
import re
import shlex
import sys
from pathlib import Path

HERE = Path(__file__).resolve().parent
LIST_PATH = HERE.parent / "deploy" / "retired-env-names.txt"
UNIT_TEMPLATE = HERE.parent / "deploy" / "audiobook-organizer.service"

# `sudo` forms that replace a file the deploy installs. Anything the helpers do
# for it is `sudo -n .../install-staged <verb>`, which none of these match.
_BYPASS = re.compile(r"\bsudo\s+(?:-\S+\s+)*(?:mv|cp|install|tee|ln|dd|rsync)\b")
_SUDO_WITHOUT_N = re.compile(r"\bsudo\s+(?!-n\b)")
# What a recipe sends over ssh: the quoted command after the host, or an
# unquoted one up to the shell's next separator. Only that is examined for
# `sudo`: an echo that mentions sudo is not a command.
_REMOTE = re.compile(r"""\bssh\s+(?:-\S+\s+)*\S+\s+(?:'([^']*)'|"([^"]*)"|([^;&|)'"]+))""")
OVERRIDE = "DEPLOY_ALLOW_NO_ROLLBACK"


def logical_lines(text: str) -> list[str]:
    """Lines as systemd joins them: a trailing backslash continues the line;
    blank lines and comments are dropped."""
    out: list[str] = []
    cur = None
    for raw in text.splitlines():
        line = raw.strip()
        if cur is None and (not line or line[0] in "#;"):
            continue
        if cur is not None and line and line[0] in "#;":
            continue
        piece = line
        if piece.endswith("\\") and (len(piece) - len(piece.rstrip("\\"))) % 2 == 1:
            cur = (cur or "") + piece[:-1] + " "
            continue
        out.append((cur or "") + piece)
        cur = None
    if cur is not None:
        out.append(cur)
    return out


def env_names(text: str) -> list[str]:
    """The variable names an Environment= line of a unit drop-in sets: every
    KEY of a KEY=value assignment, in file order. systemd splits a value on
    unquoted whitespace and takes quotes around a whole assignment off, which
    shlex does the same way; a value shlex cannot parse falls back to a plain
    split, so the check never skips a line it cannot read."""
    names: list[str] = []
    for line in logical_lines(text):
        key, sep, value = line.partition("=")
        if not sep or key.strip() != "Environment":
            continue
        try:
            words = shlex.split(value)
        except ValueError:
            words = value.split()
        for word in words:
            name, eq, _ = word.partition("=")
            if eq and name:
                names.append(name)
    return names


def parse_list(text: str) -> tuple[bool, dict[str, tuple[str, str]]]:
    """(enforced, {name: (verdict, replacement)}) from retired-env-names.txt.
    Raises ValueError for a list without the enforced marker or with a row
    that is not NAME<TAB>VERDICT<TAB>REPLACEMENT."""
    enforced = None
    rows: dict[str, tuple[str, str]] = {}
    for n, line in enumerate(text.splitlines(), 1):
        if line.startswith("# enforced:"):
            value = line.split(":", 1)[1].strip()
            if value not in ("true", "false"):
                raise ValueError(f"line {n}: `# enforced:` must say true or false")
            enforced = value == "true"
            continue
        if not line or line.startswith("#"):
            continue
        cols = line.split("\t")
        if len(cols) != 3 or cols[1] not in ("warn", "refuse") or not cols[0]:
            raise ValueError(f"line {n}: not NAME<TAB>warn|refuse<TAB>REPLACEMENT")
        rows[cols[0]] = (cols[1], cols[2])
    if enforced is None:
        raise ValueError("no `# enforced: true|false` line")
    if not rows:
        raise ValueError("no retired names")
    return enforced, rows


def _glob(pattern: str) -> "re.Pattern[str]":
    """A list pattern as a regular expression: `*` is one or more characters,
    anything else is literal (Go's envGlob, in internal/books/cutover)."""
    return re.compile("^" + ".+".join(re.escape(p) for p in pattern.split("*")) + "$")


FILE_SUFFIX = "_FILE"


def listed(name: str, rows: dict[str, tuple[str, str]]) -> str | None:
    """The list key `name` is retired under: the name itself, or the `*` pattern
    it matches (the refused operator settings, e.g. the metadata credentials
    family, whose names carry any source id). A name's <NAME>_FILE form is the
    name: core reads any env name from the file a <NAME>_FILE variable names
    (core/config/env.go), so `X_FILE` sets what `X` sets. None when it is not
    retired. The rule is the list header's, and the F3-07 rehearsal applies the
    same one to the same file."""
    candidates = [name]
    if name.endswith(FILE_SUFFIX) and len(name) > len(FILE_SUFFIX):
        candidates.append(name[: -len(FILE_SUFFIX)])
    for cand in candidates:
        if cand in rows:
            return cand
        for key in rows:
            if "*" in key and _glob(key).match(cand):
                return key
    return None


def retired_in(path: Path, rows: dict[str, tuple[str, str]]) -> tuple[list[str] | None, str]:
    """(the retired names an Environment= line of path sets, why it could not
    be read). (None, "") when the file does not exist."""
    try:
        text = path.read_text(encoding="utf-8")
    except FileNotFoundError:
        return None, ""
    except (OSError, UnicodeDecodeError) as e:
        return None, f"cannot read {path} ({e})"
    return sorted({n for n in env_names(text) if listed(n, rows) is not None}), ""


def check_retired(local_conf: Path, list_path: Path, unit_template: Path | None = None) -> int:
    """local_conf is what `make deploy` renders as the drop-in; unit_template is
    the repo's own unit, whose Environment= lines every deploy installs (the
    helper installs a copy of it as the unit). Both are the new binary's
    environment, so both are held to the list."""
    try:
        enforced, rows = parse_list(list_path.read_text(encoding="utf-8"))
    except (OSError, ValueError) as e:
        print(
            f"deploy-env-check: {list_path} is unusable ({e}). Regenerate it: "
            "go test ./internal/books/cutover -run TestRetiredEnvList -update",
            file=sys.stderr,
        )
        return 2
    sources = [local_conf] + ([unit_template] if unit_template is not None else [])
    by_file: dict[Path, list[str]] = {}
    for src in sources:
        names, why = retired_in(src, rows)
        if why:
            print(f"deploy-env-check: {why}.", file=sys.stderr)
            return 1
        if names is None and src is local_conf:
            continue
        if names:
            by_file[src] = names
    if not by_file:
        if not local_conf.exists():
            print(f"deploy-env-check: no {local_conf}: no environment to check.")
        else:
            print(f"deploy-env-check: {local_conf} sets no retired env name.")
        return 0
    lines = []
    count = 0
    for src, found in by_file.items():
        count += len(found)
        for name in found:
            verdict, replacement = rows[listed(name, rows)]
            what = (
                "the new binary refuses to boot on it"
                if verdict == "refuse"
                else "the new binary ignores it with a warning, so the setting silently stops applying"
            )
            use = f"; set {replacement} instead" if replacement != "-" else "; it became no setting"
            lines.append(f"  {src}: {name}: {what}{use}")
    where = " and ".join(str(f) for f in by_file)
    if not enforced:
        print(
            f"deploy-env-check: {where} set {count} name(s) the flip retires. The binary "
            "built from this tree still reads them, so the deploy is not refused; they must be "
            "renamed before the deploy that carries the flip:\n" + "\n".join(lines)
        )
        return 0
    print(
        f"deploy-env-check: REFUSING: {where} set {count} env name(s) the binary being "
        "deployed does not read (deploy/retired-env-names.txt, generated from the rename table). "
        "Nothing was uploaded or installed:\n" + "\n".join(lines),
        file=sys.stderr,
    )
    return 1


def recipes(makefile: str) -> dict[str, tuple[list[str], str]]:
    """target -> (prerequisites, recipe text) for every rule with at least one
    recipe line. A backslash-newline joins lines, as make does (the next
    line's leading tab goes with it). Variable assignments (`:=`) are not
    rules."""
    out: dict[str, tuple[list[str], str]] = {}
    lines = re.sub(r"\\\n\t?", " ", makefile).splitlines()
    i = 0
    while i < len(lines):
        m = re.match(r"^([A-Za-z0-9_.-]+)\s*:(?!=)\s*([^#]*)", lines[i])
        i += 1
        if not m:
            continue
        body = []
        while i < len(lines) and (lines[i].startswith("\t") or not lines[i].strip()):
            if lines[i].startswith("\t"):
                body.append(lines[i][1:])
            i += 1
        if body:
            out[m.group(1)] = (m.group(2).split(), "\n".join(body))
    return out


def list_enforced(list_path: Path) -> tuple[bool, str]:
    """(whether the retired-name list says `enforced: true`, why it is unknown).
    A list that cannot be read counts as enforced: the override that skips the
    rollback state must not be honoured on a build whose state is unknown."""
    try:
        return parse_list(list_path.read_text(encoding="utf-8"))[0], ""
    except (OSError, ValueError) as e:
        return True, f"{list_path} is unusable ({e})"


def check_makefile_local(path: Path, environ: dict[str, str], list_path: Path = LIST_PATH) -> int:
    try:
        text = path.read_text(encoding="utf-8")
    except FileNotFoundError:
        print(f"deploy-env-check: no {path}: no deploy recipe to check.")
        return 0
    except (OSError, UnicodeDecodeError) as e:
        print(f"deploy-env-check: cannot read {path} ({e}).", file=sys.stderr)
        return 1
    rules = recipes(text)
    problems: list[str] = []
    for target in ("deploy", "deploy-debug"):
        if target not in rules:
            continue
        prereqs, body = rules[target]
        if target == "deploy-debug" and "deploy" in prereqs and "deploy" in rules:
            continue  # runs deploy's recipe, which is checked on its own
        remote = "\n".join("".join(groups) for groups in _REMOTE.findall(body))
        if _BYPASS.search(remote):
            problems.append(
                f"`{target}` replaces a file with `sudo mv/cp/install/tee`: the previous binary, unit "
                "and local.conf are not kept"
            )
        elif _SUDO_WITHOUT_N.search(remote):
            problems.append(
                f"`{target}` runs `sudo` without -n, which cannot run over a tty-less ssh"
            )
        if "install-staged binary" not in body:
            problems.append(
                f"`{target}` never runs `install-staged binary`, the step that keeps the previous "
                "binary as .prev together with the unit and local.conf it ran under"
            )
    if not problems:
        print(f"deploy-env-check: {path}'s deploy recipes go through the install-staged helper.")
        for target in ("deploy", "deploy-debug"):
            body = rules[target][1] if target in rules else ""
            if "install-staged binary" in body and "deploy_generation_args" not in body:
                print(
                    f"deploy-env-check: NOTE: `{target}` installs the binary without telling the helper "
                    "its version and store levels (scripts/deploy_generation_args.py, see "
                    "Makefile.local.example). Every kept environment then counts as unknown and is "
                    "pinned, so a hotfix train keeps more of them than it needs to."
                )
        return 0
    detail = "\n".join(f"  {p}" for p in problems)
    owner_step = (
        "Install the helper bundle on the host FIRST (once, as the owner: `make install-rollback-helpers`, "
        'docs/system/deploy-and-gpu-ops.md, "Rollback helpers"), THEN merge Makefile.local.example\'s deploy and '
        "deploy-debug recipes into this file (keep your own variables): they install the binary, the unit and "
        "local.conf through the root-owned install-staged helper, which keeps the previous ones. A "
        "recipe that calls the helper before it is installed fails at its first step."
    )
    if environ.get(OVERRIDE, "").strip() == "1":
        enforced, why = list_enforced(list_path)
        if enforced:
            reason = (
                why
                or f"{list_path.name} says `enforced: true`: this is the build that carries the flip"
            )
            print(
                f"deploy-env-check: REFUSING: {OVERRIDE}=1 is not honoured for this build ({reason}). "
                "The flip cannot be undone with the binary alone: the previous release reads "
                "environment names this one retires, so a deploy that keeps no rollback state strands "
                f"it. {path} keeps no rollback state:\n{detail}\n{owner_step} "
                "Nothing was uploaded or installed.",
                file=sys.stderr,
            )
            return 1
        print(
            f"deploy-env-check: WARNING: {OVERRIDE}=1: deploying although {path} keeps no rollback "
            f"state:\n{detail}\n  This deploy keeps neither the binary it replaces nor that binary's unit "
            "and local.conf. If an earlier deploy went through the helper, `make rollback` after this "
            "one goes back to THAT release (the one kept beside <bin>.prev then), skipping the release "
            "this deploy replaces; if none did, `make rollback` refuses.",
            file=sys.stderr,
        )
        return 0
    print(
        f"deploy-env-check: REFUSING: {path} keeps no rollback state, so a rollback after this deploy "
        f"would put back the binary alone (or nothing):\n{detail}\n{owner_step} Nothing was uploaded "
        f"or installed. To deploy anyway, while the list says `enforced: false`: {OVERRIDE}=1 make deploy.",
        file=sys.stderr,
    )
    return 1


def summary() -> str | None:
    """The first line of the module docstring, the --help summary. None when
    the interpreter dropped docstrings (python -OO), which the caller treats as
    an unusable run rather than printing a blank help."""
    doc = __doc__
    if doc is None:
        return None
    return doc.splitlines()[0]


def main(argv: list[str] | None = None, environ: dict[str, str] | None = None) -> int:
    description = summary()
    if description is None:
        # Fail closed: a deploy gate that cannot read its own description is
        # not running the way it was tested (python -OO strips docstrings), so
        # it must not be taken as having passed.
        print(
            "deploy-env-check: the module docstring is missing (python -OO strips it); "
            "run it as `python3 scripts/deploy_env_check.py`, without -OO. Nothing was checked.",
            file=sys.stderr,
        )
        return 2
    p = argparse.ArgumentParser(description=description)
    p.add_argument("checks", nargs="*", choices=["retired", "makefile-local"])
    p.add_argument("--local-conf", default="deploy/local.conf")
    p.add_argument("--unit-template", default=str(UNIT_TEMPLATE))
    p.add_argument("--list", default=str(LIST_PATH))
    p.add_argument("--makefile-local", default="Makefile.local")
    a = p.parse_args(argv)
    env = os.environ if environ is None else environ
    todo = a.checks or ["retired", "makefile-local"]
    worst = 0
    if "retired" in todo:
        worst = max(worst, check_retired(Path(a.local_conf), Path(a.list), Path(a.unit_template)))
    if "makefile-local" in todo:
        worst = max(worst, check_makefile_local(Path(a.makefile_local), dict(env), Path(a.list)))
    return worst


if __name__ == "__main__":
    sys.exit(main())
