#!/usr/bin/env python3
# file: scripts/test_build_taglib_static.py
# version: 1.0.0
# guid: b7c3e9d5-2a64-4f18-9e0b-6d1f4a8c2e73
# last-edited: 2026-10-11

"""Unit tests for scripts/ci/build_taglib_static.py (no network, no compiler).

Run with:  python3 -m unittest discover -s scripts -p 'test_build_taglib_static.py' -v
"""

import hashlib
import importlib.util
import sys
import tempfile
import unittest
from pathlib import Path
from unittest import mock

_HERE = Path(__file__).resolve().parent
_spec = importlib.util.spec_from_file_location(
    "build_taglib_static", _HERE / "ci" / "build_taglib_static.py"
)
bts = importlib.util.module_from_spec(_spec)
assert _spec.loader is not None
sys.modules["build_taglib_static"] = bts
_spec.loader.exec_module(bts)


def populate(root: Path, skip: str | None = None) -> None:
    for rel in bts.REQUIRED_FILES:
        if rel == skip:
            continue
        (root / rel).parent.mkdir(parents=True, exist_ok=True)
        (root / rel).write_bytes(b"x")


class PinsMatchTheDockerfile(unittest.TestCase):
    """CI must test the TagLib production builds: the pins here and in the
    Dockerfile move together."""

    def test_versions_and_checksums(self) -> None:
        dockerfile = (_HERE.parent / "Dockerfile").read_text()
        for needle in (
            f"taglib/releases/download/v{bts.TAGLIB_VERSION}/taglib-{bts.TAGLIB_VERSION}.tar.gz",
            bts.TAGLIB_SHA256,
            f"nemtrif/utfcpp/archive/refs/tags/v{bts.UTFCPP_VERSION}.tar.gz",
            bts.UTFCPP_SHA256,
        ):
            self.assertIn(needle, dockerfile)


class Cache(unittest.TestCase):
    def test_key_names_both_versions(self) -> None:
        self.assertEqual(
            bts.cache_key(), f"taglib-{bts.TAGLIB_VERSION}-utfcpp-{bts.UTFCPP_VERSION}"
        )

    def test_is_complete_needs_every_file(self) -> None:
        for skip in bts.REQUIRED_FILES:
            with tempfile.TemporaryDirectory() as d:
                populate(Path(d), skip=skip)
                self.assertFalse(bts.is_complete(Path(d)), skip)
        with tempfile.TemporaryDirectory() as d:
            populate(Path(d))
            self.assertTrue(bts.is_complete(Path(d)))

    def test_hit_does_not_build(self) -> None:
        with tempfile.TemporaryDirectory() as d:
            final = Path(d) / "taglib" / bts.cache_key()
            populate(final)
            with mock.patch.object(bts, "build", side_effect=AssertionError("built on a hit")):
                self.assertEqual(bts.ensure_cached(Path(d)), final)

    def test_miss_builds_once_and_publishes(self) -> None:
        with tempfile.TemporaryDirectory() as d:
            calls = []

            def fake_build(stage: Path, work: Path) -> None:
                calls.append(stage)
                populate(stage)

            with mock.patch.object(bts, "build", fake_build):
                first = bts.ensure_cached(Path(d))
                second = bts.ensure_cached(Path(d))
            self.assertEqual(first, second)
            self.assertTrue(bts.is_complete(first))
            self.assertEqual(len(calls), 1, "the second call is a cache hit")
            leftovers = [
                p.name for p in (Path(d) / "taglib").iterdir() if p.name != bts.cache_key()
            ]
            self.assertEqual(leftovers, [], "the build directory is removed")

    def test_incomplete_cache_entry_is_rebuilt_not_trusted(self) -> None:
        with tempfile.TemporaryDirectory() as d:
            final = Path(d) / "taglib" / bts.cache_key()
            populate(final, skip="lib/libtag_c.a")
            built = []

            def fake_build(stage: Path, work: Path) -> None:
                built.append(1)
                populate(stage)

            with mock.patch.object(bts, "build", fake_build):
                self.assertEqual(bts.ensure_cached(Path(d)), final)
            self.assertEqual(built, [1])
            self.assertTrue(bts.is_complete(final))


class Checksum(unittest.TestCase):
    def test_mismatch_is_fatal(self) -> None:
        with tempfile.TemporaryDirectory() as d:
            p = Path(d) / "a.tar.gz"
            p.write_bytes(b"payload")
            bts.verify_checksum(p, hashlib.sha256(b"payload").hexdigest())
            with self.assertRaises(SystemExit):
                bts.verify_checksum(p, "0" * 64)


class Install(unittest.TestCase):
    def test_copies_libs_and_header(self) -> None:
        with tempfile.TemporaryDirectory() as src, tempfile.TemporaryDirectory() as dest:
            populate(Path(src))
            bts.install_into(Path(src), Path(dest) / "third_party" / "taglib")
            for rel in bts.REQUIRED_FILES:
                self.assertTrue((Path(dest) / "third_party" / "taglib" / rel).is_file(), rel)

    def test_no_cache_root_is_an_error(self) -> None:
        with mock.patch.dict("os.environ", {}, clear=True):
            self.assertEqual(bts.main([]), 2)


if __name__ == "__main__":
    unittest.main()
