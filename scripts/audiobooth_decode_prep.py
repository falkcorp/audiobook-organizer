#!/usr/bin/env python3
# file: scripts/audiobooth_decode_prep.py
# version: 1.0.0
# guid: 6f2b8d41-93c7-4e0a-b5d2-17a9c3e8f604
# last-edited: 2026-09-25
"""Fetch the pinned AudioBooth commit and stage its Swift models for the decode proof.

The decode proof (tests/audiobooth-decode) compiles AudioBooth's OWN model sources and
decodes our ABS handler responses through them. No third-party code is committed:

1. Read the pinned repo + SHA from tests/audiobooth-decode/audiobooth.pin.
2. Shallow-fetch exactly that commit into the gitignored .cache/audiobooth/src and fail
   unless HEAD equals the pin afterwards.
3. Check that NetworkService.send() still decodes the way the harness mirrors it
   (epoch-millisecond dates, Data skips decoding, an empty body throws). If upstream
   changed that, the harness decoder is stale and the proof would be meaningless.
4. Copy API/Sources/API/Models/*.swift (minus the app-state files that cannot compile
   without the iOS-only dependencies) into the gitignored
   tests/audiobooth-decode/Sources/AudioBoothModels/Upstream/.
5. Extract `public struct Page<T>` out of Audiobookshelf.swift. That file imports Nuke,
   so it cannot be compiled whole, but Page<T> is the decode type of every paginated
   call site and must be the upstream definition, not a hand copy.
"""

from __future__ import annotations

import re
import shutil
import subprocess
import sys
from pathlib import Path

ROOT = Path(__file__).resolve().parent.parent
HARNESS = ROOT / "tests" / "audiobooth-decode"
PIN = HARNESS / "audiobooth.pin"
CACHE = ROOT / ".cache" / "audiobooth" / "src"
STAGE = HARNESS / "Sources" / "AudioBoothModels" / "Upstream"

# Model files that are app state, not response models, and need the iOS-only
# dependencies (Keychain storage, @Observable server objects) to compile.
EXCLUDED = {
    "Server.swift": "app state: @Observable server object backed by Keychain storage",
}

# Lines NetworkService.swift must still contain for the harness decoder to be faithful.
DECODER_INVARIANTS = [
    "let timestamp = try container.decode(Int64.self)",
    "return Date(timeIntervalSince1970: TimeInterval(timestamp / 1000))",
    "if T.self == Data.self {",
    "} else if data.isEmpty {",
]


def read_pin() -> tuple[str, str]:
    values: dict[str, str] = {}
    for line in PIN.read_text().splitlines():
        line = line.strip()
        if not line or line.startswith("#"):
            continue
        key, _, value = line.partition("=")
        values[key.strip()] = value.strip()
    if not re.fullmatch(r"[0-9a-f]{40}", values.get("sha", "")):
        sys.exit(f"{PIN}: sha must be a full 40-character commit id")
    return values["repo"], values["sha"]


def git(*args: str) -> str:
    return subprocess.run(
        ["git", "-C", str(CACHE), *args], check=True, capture_output=True, text=True
    ).stdout.strip()


def head() -> str:
    try:
        return git("rev-parse", "HEAD")
    except subprocess.CalledProcessError:
        return ""


def fetch(repo: str, sha: str) -> None:
    if (CACHE / ".git").is_dir() and head() == sha:
        print(f"cache already at {sha}")
        return
    CACHE.mkdir(parents=True, exist_ok=True)
    if not (CACHE / ".git").is_dir():
        git("init", "-q")
    print(f"fetching {repo}@{sha} (depth 1)")
    git("fetch", "-q", "--depth", "1", repo, sha)
    git("checkout", "-q", "--force", "FETCH_HEAD")
    if head() != sha:
        sys.exit(f"cache HEAD is {head()!r}, pin says {sha}")


def check_decoder() -> None:
    src = (CACHE / "API/Sources/API/NetworkService.swift").read_text()
    missing = [line for line in DECODER_INVARIANTS if line not in src]
    if missing:
        sys.exit(
            "upstream NetworkService.swift no longer contains:\n  "
            + "\n  ".join(missing)
            + "\nUpdate Tests/DecodeTests/AppDecoder.swift to match before trusting the proof."
        )


def extract_page() -> str:
    src = (CACHE / "API/Sources/API/Audiobookshelf.swift").read_text()
    start = src.find("public struct Page<")
    if start < 0:
        sys.exit("Audiobookshelf.swift no longer declares public struct Page<T>")
    depth = 0
    for i in range(src.find("{", start), len(src)):
        if src[i] == "{":
            depth += 1
        elif src[i] == "}":
            depth -= 1
            if depth == 0:
                return (
                    "// Extracted verbatim from AudioBooth API/Sources/API/Audiobookshelf.swift\n"
                    "// by scripts/audiobooth_decode_prep.py. Do not edit; do not commit.\n"
                    "import Foundation\n\n" + src[start : i + 1] + "\n"
                )
    sys.exit("could not find the end of Page<T> in Audiobookshelf.swift")


def stage() -> None:
    if STAGE.exists():
        shutil.rmtree(STAGE)
    STAGE.mkdir(parents=True)
    models = sorted((CACHE / "API/Sources/API/Models").glob("*.swift"))
    for model in models:
        if model.name in EXCLUDED:
            print(f"  excluded {model.name}: {EXCLUDED[model.name]}")
            continue
        shutil.copy2(model, STAGE / model.name)
    (STAGE / "Page.swift").write_text(extract_page())
    print(f"staged {len(models) - len(EXCLUDED)} model files + Page.swift into {STAGE}")


def main() -> None:
    repo, sha = read_pin()
    fetch(repo, sha)
    check_decoder()
    stage()


if __name__ == "__main__":
    main()
