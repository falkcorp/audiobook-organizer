#!/usr/bin/env python3
# file: scripts/abs_capture_fixtures.py
# version: 1.0.0
# guid: 2a9c6e04-8d71-4f36-b920-5e83c1740af6
# last-edited: 2026-07-29
"""Capture golden API fixtures from the reference Audiobookshelf oracle.

The published ABS API docs are stale, so the running server is the only
trustworthy spec. This walks the endpoint surface we intend to implement and
records each request/response pair verbatim. Normalization happens later, in
Go, at compare time -- fixtures stay faithful to what ABS actually returned.

Non-200 responses are recorded, not hidden: a 404 here is real information
about the 2.36.x surface and must correct the spec rather than be papered over.

Usage:
    python3 scripts/abs_capture_fixtures.py \
        --base-url http://localhost:13378 \
        --username oracle --password oracle-dev-only
"""

from __future__ import annotations

import argparse
import json
import pathlib
import re
import sys

try:
    import requests
except ImportError:
    sys.exit("requests is required: python3 -m pip install requests")

FIXTURE_DIR = pathlib.Path(__file__).resolve().parent.parent / "testdata" / "abs-fixtures"

# Response headers worth preserving: these drive client caching and seeking.
KEPT_HEADERS = ("content-type", "accept-ranges", "etag", "cache-control", "content-range")

# Keys whose values are credentials. Fixtures are committed to a public repo, and
# conformance only needs a field's TYPE and PRESENCE -- the normalizer canonicalizes
# these values at compare time anyway -- so redacting on disk costs nothing and
# avoids committing token-shaped strings that trip secret scanners.
SECRET_KEYS = frozenset(
    {"accesstoken", "refreshtoken", "token", "password", "apikey", "secret", "jwtsecret"}
)
REDACTED = "<redacted>"


def redact(value):
    """Recursively replace credential values, preserving JSON type."""
    if isinstance(value, dict):
        return {
            k: (REDACTED if k.lower() in SECRET_KEYS and isinstance(v, str) else redact(v))
            for k, v in value.items()
        }
    if isinstance(value, list):
        return [redact(v) for v in value]
    return value


def slugify(method: str, path: str) -> str:
    """Build a stable filename from a method and path.

    Concrete ids are stripped so the filename stays stable across recaptures
    (ids are volatile), e.g. /api/items/<uuid>/play -> post_api_items_id_play.
    """
    generic = re.sub(
        r"/(?:[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{12})",
        "/id",
        path,
    )
    generic = generic.split("?", 1)[0]
    slug = re.sub(r"[^a-zA-Z0-9]+", "_", generic).strip("_").lower()
    return f"{method.lower()}_{slug or 'root'}.json"


def write_fixture(method: str, path: str, body, resp) -> None:
    """Persist one request/response pair."""
    try:
        parsed = resp.json()
    except ValueError:
        parsed = {"__non_json_body__": resp.text[:2000]}

    fixture = {
        "request": {"method": method, "path": path, "body": redact(body)},
        "response": {
            "status": resp.status_code,
            "headers": {
                k.lower(): v for k, v in resp.headers.items() if k.lower() in KEPT_HEADERS
            },
            "body": redact(parsed),
        },
    }

    FIXTURE_DIR.mkdir(parents=True, exist_ok=True)
    out = FIXTURE_DIR / slugify(method, path)
    out.write_text(json.dumps(fixture, indent=2, sort_keys=True) + "\n")
    flag = "" if resp.ok else "   <-- NON-200, verify against spec"
    print(f"  {resp.status_code}  {method:5s} {path[:64]:64s} -> {out.name}{flag}")


def main() -> int | str:
    ap = argparse.ArgumentParser()
    ap.add_argument("--base-url", default="http://localhost:13378")
    ap.add_argument("--username", default="oracle")
    ap.add_argument("--password", default="oracle-dev-only")
    args = ap.parse_args()

    base = args.base_url.rstrip("/")
    sess = requests.Session()

    print("== discovery (pre-auth) ==")
    for path in ("/ping", "/status"):
        write_fixture("GET", path, None, sess.get(f"{base}{path}", timeout=30))

    print("== auth ==")
    login_body = {"username": args.username, "password": args.password}
    # Mobile clients send x-return-tokens to get the refresh token in the body.
    login = sess.post(
        f"{base}/login", json=login_body, headers={"x-return-tokens": "true"}, timeout=30
    )
    write_fixture("POST", "/login", login_body, login)
    if login.status_code != 200:
        return f"login failed ({login.status_code}); is the oracle initialized?"

    payload = login.json()
    user = payload.get("user", {})
    # 2.36.0 nests BOTH tokens inside `user` (accessToken/refreshToken), and also
    # keeps a legacy `user.token`. Prefer accessToken, fall back to token.
    token = user.get("accessToken") or user.get("token")
    refresh = user.get("refreshToken")
    if not token:
        return f"no access token in login response; user keys: {sorted(user)}"
    sess.headers["Authorization"] = f"Bearer {token}"

    if refresh:
        write_fixture(
            "POST",
            "/auth/refresh",
            None,
            sess.post(
                f"{base}/auth/refresh",
                headers={"x-refresh-token": refresh},
                timeout=30,
            ),
        )

    print("== user ==")
    for path in ("/api/me", "/api/me/progress", "/api/me/sessions"):
        write_fixture("GET", path, None, sess.get(f"{base}{path}", timeout=30))

    print("== libraries ==")
    libs = sess.get(f"{base}/api/libraries", timeout=30)
    write_fixture("GET", "/api/libraries", None, libs)
    library_ids = [lib["id"] for lib in libs.json().get("libraries", [])]
    if not library_ids:
        return "no libraries on the oracle; add one pointing at /audiobooks"

    item_id = None
    for lib_id in library_ids:
        for suffix in (
            "items?limit=10&page=0",
            "personalized",
            "series",
            "authors",
            "narrators",
            "search?q=odyssey",
            "filterdata",
        ):
            path = f"/api/libraries/{lib_id}/{suffix}"
            write_fixture("GET", path, None, sess.get(f"{base}{path}", timeout=60))
        if item_id is None:
            items = sess.get(
                f"{base}/api/libraries/{lib_id}/items?limit=10&page=0", timeout=60
            )
            for result in items.json().get("results", []):
                # Prefer the multi-file book: it exercises the cumulative
                # startOffset timeline, the harder of the two shapes.
                if (result.get("media") or {}).get("numAudioFiles", 0) > 1:
                    item_id = result["id"]
                    break
            if item_id is None and items.json().get("results"):
                item_id = items.json()["results"][0]["id"]

    if item_id is None:
        return "no library items found; did the oracle finish scanning?"

    print("== item detail + playback ==")
    detail = f"/api/items/{item_id}?expanded=1&include=progress"
    write_fixture("GET", detail, None, sess.get(f"{base}{detail}", timeout=30))

    play_body = {
        "deviceInfo": {"clientName": "conformance-capture", "deviceId": "capture-001"},
        "mediaPlayer": "unknown",
        "forceDirectPlay": True,
    }
    play = sess.post(f"{base}/api/items/{item_id}/play", json=play_body, timeout=60)
    write_fixture("POST", f"/api/items/{item_id}/play", play_body, play)

    if play.ok:
        session_id = play.json().get("id")
        if session_id:
            sync_body = {"currentTime": 12.5, "timeListened": 10, "duration": 9975.48}
            write_fixture(
                "POST",
                f"/api/session/{session_id}/sync",
                sync_body,
                sess.post(
                    f"{base}/api/session/{session_id}/sync", json=sync_body, timeout=30
                ),
            )
            write_fixture(
                "POST",
                f"/api/session/{session_id}/close",
                None,
                sess.post(f"{base}/api/session/{session_id}/close", timeout=30),
            )

    print("== progress + bookmarks ==")
    prog_body = {
        "currentTime": 42.0,
        "duration": 9975.48,
        "progress": 0.004,
        "isFinished": False,
    }
    write_fixture(
        "PATCH",
        f"/api/me/progress/{item_id}",
        prog_body,
        sess.patch(f"{base}/api/me/progress/{item_id}", json=prog_body, timeout=30),
    )

    bm_body = {"time": 100, "title": "conformance bookmark"}
    write_fixture(
        "POST",
        f"/api/me/item/{item_id}/bookmark",
        bm_body,
        sess.post(f"{base}/api/me/item/{item_id}/bookmark", json=bm_body, timeout=30),
    )
    write_fixture(
        "GET",
        f"/api/me/bookmarks/{item_id}",
        None,
        sess.get(f"{base}/api/me/bookmarks/{item_id}", timeout=30),
    )

    # Re-capture /api/me AFTER progress and a bookmark exist, so the fixture
    # shows populated mediaProgress[]/bookmarks[] rather than empty arrays.
    write_fixture(
        "GET",
        "/api/me?populated=1",
        None,
        sess.get(f"{base}/api/me", timeout=30),
    )

    print(f"\nWrote fixtures to {FIXTURE_DIR}")
    return 0


if __name__ == "__main__":
    result = main()
    if isinstance(result, str):
        sys.exit(f"ERROR: {result}")
    sys.exit(result)
