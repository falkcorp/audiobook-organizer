#!/usr/bin/env python3
# file: scripts/ci/build_taglib_static.py
# version: 1.0.0
# guid: 4d9a7e12-6b35-4c80-a1f7-3e8c5d2b9f40
# last-edited: 2026-10-11
"""Install static TagLib 2.0.2 into third_party/taglib for `-tags native_taglib`.

The production binary links TagLib statically (Dockerfile, Dockerfile.build-cgo)
and taglib_cgo.go's #cgo directives look for the libraries in
third_party/taglib/lib, which is not checked in. A Go test run with
`-tags native_taglib` therefore needs them built first. Distro packages do not
do: Debian and Ubuntu's libtag1-dev is TagLib 1.x, which has a different tag_c
API, so a build against it would test something production never runs.

This script builds TagLib once into the host-wide CI cache, keyed by the TagLib
and utfcpp versions, and copies the result into third_party/taglib. A second run
with the same versions only copies. The recipe is the Dockerfile's: the same
pinned releases (sha256-checked), the same cmake flags, system static zlib.

Requirements on the host: cmake, make, a C and C++ compiler, network access to
github.com, and libz.a (Debian/Ubuntu: zlib1g-dev). The Linux link flags in
taglib_cgo.go (-lstdc++ -lgcc -lgcc_eh) mean the result only links on Linux.

Usage:
  build_taglib_static.py [--cache-root DIR] [--dest third_party/taglib]

--cache-root defaults to $CI_CACHE_ROOT (set by scripts/ci/cache_env.sh).
"""

from __future__ import annotations

import argparse
import hashlib
import os
import shutil
import subprocess
import sys
import tarfile
import tempfile
import urllib.request
from pathlib import Path

TAGLIB_VERSION = "2.0.2"
TAGLIB_SHA256 = "0de288d7fe34ba133199fd8512f19cc1100196826eafcb67a33b224ec3a59737"
TAGLIB_URL = f"https://github.com/taglib/taglib/releases/download/v{TAGLIB_VERSION}/taglib-{TAGLIB_VERSION}.tar.gz"

UTFCPP_VERSION = "4.0.6"
UTFCPP_SHA256 = "6920a6a5d6a04b9a89b2a89af7132f8acefd46e0c2a7b190350539e9213816c0"
UTFCPP_URL = f"https://github.com/nemtrif/utfcpp/archive/refs/tags/v{UTFCPP_VERSION}.tar.gz"

# What the build must leave behind, relative to the install root.
REQUIRED_FILES = ("lib/libtag.a", "lib/libtag_c.a", "lib/libz.a", "include/tag_c.h")


def cache_key() -> str:
    """The cache directory name: any version change builds again."""
    return f"taglib-{TAGLIB_VERSION}-utfcpp-{UTFCPP_VERSION}"


def is_complete(root: Path) -> bool:
    return all((root / rel).is_file() for rel in REQUIRED_FILES)


def sha256_of(path: Path) -> str:
    h = hashlib.sha256()
    with path.open("rb") as f:
        for chunk in iter(lambda: f.read(1 << 20), b""):
            h.update(chunk)
    return h.hexdigest()


def verify_checksum(path: Path, want: str) -> None:
    got = sha256_of(path)
    if got != want:
        raise SystemExit(f"checksum mismatch for {path.name}: got {got}, want {want}")


def download(url: str, want_sha256: str, dest: Path) -> None:
    print(f"downloading {url}")
    with urllib.request.urlopen(url, timeout=120) as resp, dest.open("wb") as out:  # noqa: S310
        shutil.copyfileobj(resp, out)
    verify_checksum(dest, want_sha256)


def run(cmd: list[str], cwd: Path) -> None:
    """Run a build step; on failure show the tail of its output (the Dockerfile
    discards it, which makes a broken build undiagnosable)."""
    proc = subprocess.run(
        cmd, cwd=cwd, stdout=subprocess.PIPE, stderr=subprocess.STDOUT, text=True, check=False
    )
    if proc.returncode != 0:
        tail = "\n".join(proc.stdout.splitlines()[-40:])
        raise SystemExit(f"{' '.join(cmd)} failed ({proc.returncode}):\n{tail}")


def find_static_zlib() -> Path:
    """The system's libz.a, as the C compiler would link it."""
    cc = os.environ.get("CC", "cc")
    out = subprocess.run(
        [cc, "-print-file-name=libz.a"], capture_output=True, text=True, check=False
    ).stdout.strip()
    # An unknown file is echoed back as the bare name.
    if out and os.path.isabs(out) and os.path.isfile(out):
        return Path(out)
    raise SystemExit(f"libz.a not found for {cc} (Debian/Ubuntu: apt install zlib1g-dev)")


def build(stage: Path, work: Path) -> None:
    """Build TagLib and zlib into stage/{lib,include}."""
    for tool in ("cmake", "make", "cc", "c++"):
        if shutil.which(tool) is None:
            raise SystemExit(
                f"{tool} not found in PATH; the CI host needs cmake and build-essential"
            )

    utf_tar, tag_tar = work / "utfcpp.tar.gz", work / "taglib.tar.gz"
    download(UTFCPP_URL, UTFCPP_SHA256, utf_tar)
    download(TAGLIB_URL, TAGLIB_SHA256, tag_tar)
    for archive in (utf_tar, tag_tar):
        with tarfile.open(archive) as tf:
            # The archives are checksum-verified above; the data filter (Python
            # 3.10.12+/3.11.4+/3.12) is defence in depth where it exists.
            if hasattr(tarfile, "data_filter"):
                tf.extractall(work, filter="data")
            else:
                tf.extractall(work)  # noqa: S202

    install = work / "install"
    build_dir = work / "build"
    build_dir.mkdir()
    run(
        [
            "cmake",
            str(work / f"taglib-{TAGLIB_VERSION}"),
            f"-DCMAKE_INSTALL_PREFIX={install}",
            "-DBUILD_SHARED_LIBS=OFF",
            "-DBUILD_EXAMPLES=OFF",
            "-DBUILD_TESTING=OFF",
            "-DWITH_ZLIB=ON",
            f"-Dutf8cpp_INCLUDE_DIR={work / f'utfcpp-{UTFCPP_VERSION}' / 'source'}",
            "-DCMAKE_C_FLAGS=-fPIC",
            "-DCMAKE_CXX_FLAGS=-fPIC",
        ],
        build_dir,
    )
    run(["make", f"-j{os.cpu_count() or 2}"], build_dir)
    run(["make", "install"], build_dir)

    (stage / "lib").mkdir(parents=True)
    (stage / "include").mkdir()
    for lib in ("libtag.a", "libtag_c.a"):
        shutil.copy2(install / "lib" / lib, stage / "lib" / lib)
    shutil.copy2(find_static_zlib(), stage / "lib" / "libz.a")
    shutil.copy2(install / "include" / "taglib" / "tag_c.h", stage / "include" / "tag_c.h")
    if not is_complete(stage):
        raise SystemExit(f"TagLib build left {stage} incomplete")


def ensure_cached(cache_root: Path) -> Path:
    """Return the cached install root, building it if it is not there yet."""
    parent = cache_root / "taglib"
    parent.mkdir(parents=True, exist_ok=True)
    final = parent / cache_key()
    if is_complete(final):
        print(f"TagLib {TAGLIB_VERSION} cache hit: {final}")
        return final
    print(f"TagLib {TAGLIB_VERSION} cache miss: building into {final}")
    work = Path(tempfile.mkdtemp(prefix="build-", dir=parent))
    try:
        stage = work / "stage"
        build(stage, work)
        # An incomplete entry (an interrupted copy by an older version of this
        # script, a hand edit) would make every later rename fail and wedge
        # CI: clear it.
        if final.exists() and not is_complete(final):
            shutil.rmtree(final)
        # Publish atomically. Another pipeline that built the same key first
        # makes rename fail (the target is a non-empty directory); its copy is
        # as good as ours.
        try:
            os.rename(stage, final)
        except OSError:
            if not is_complete(final):
                raise
    finally:
        shutil.rmtree(work, ignore_errors=True)
    return final


def install_into(src: Path, dest: Path) -> None:
    """Copy the cached libraries and header to dest/lib and dest/include."""
    for rel in REQUIRED_FILES:
        target = dest / rel
        target.parent.mkdir(parents=True, exist_ok=True)
        shutil.copy2(src / rel, target)
    print(f"installed TagLib {TAGLIB_VERSION} into {dest}")


def main(argv: list[str] | None = None) -> int:
    ap = argparse.ArgumentParser(
        description=__doc__, formatter_class=argparse.RawDescriptionHelpFormatter
    )
    ap.add_argument(
        "--cache-root",
        default=os.environ.get("CI_CACHE_ROOT"),
        help="host-wide CI cache (default $CI_CACHE_ROOT)",
    )
    ap.add_argument(
        "--dest",
        default="third_party/taglib",
        help="where taglib_cgo.go looks for lib/ and include/",
    )
    args = ap.parse_args(argv)
    if not args.cache_root:
        print("no cache root: pass --cache-root or source scripts/ci/cache_env.sh", file=sys.stderr)
        return 2
    install_into(ensure_cached(Path(args.cache_root)), Path(args.dest))
    return 0


if __name__ == "__main__":
    sys.exit(main())
