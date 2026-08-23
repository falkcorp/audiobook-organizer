# file: scripts/tests/__init__.py
# version: 1.0.0
# guid: 1a3cbab2-5a6f-4413-8e8e-c1e78cad0914
# last-edited: 2026-08-23

"""Package marker.

CI wires `python3 -m unittest discover -s scripts -p 'test_*.py'`
(.github/workflows/ci.yml). unittest's discover only recurses into a
subdirectory when it is an importable package, so without this file
scripts/tests/test_*.py would be silently skipped -- 0 tests run, no
error -- rather than fail loudly. Verified locally: discovery finds
nothing under scripts/tests without this marker and finds it correctly
once this file is present.
"""
