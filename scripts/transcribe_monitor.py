#!/usr/bin/env python3
# file: scripts/transcribe_monitor.py
# version: 1.4.0
# guid: 2f7c4a91-8b03-4d6e-9a52-1c6e8f0b3d47
# last-edited: 2026-10-22
"""Transcription progress monitor + alerter.

Polls the audiobook-organizer transcribe-stats aggregate
(GET /api/v1/maintenance/transcribe-stats) on a fixed interval, writes one
parseable JSONL metrics record per poll, prints a human summary, and raises
ALERTs when:

  * IDLE      — no run has ever produced stats, or the last run is marked done
                and nothing new is happening (optionally auto-relaunch).
  * STALL     — a run is in-flight but its counters haven't advanced for
                --stall-secs (the GPU/op is stuck).
  * NOPROGRESS— a run completed but produced almost no OK transcriptions
                (the "did-nothing completion" pattern: mostly source_file_missing).
  * WHISPER_DOWN — the Whisper server /health check fails.
  * RELAUNCH_IS_PREVIEW — an auto-relaunch came back as a preview run (its
                progress message opens with the PREVIEW marker): it
                transcribes nothing, writes no stats, and the next IDLE poll
                would relaunch another one. Raised loudly (ERROR plus a banner
                on stderr) because the server, not this script, decided the
                mode.
  * RELAUNCH_UNVERIFIED — a relaunched run settled, or stayed silent past
                --relaunch-verify-max, without a message carrying either mode
                marker (for example a server build older than the markers).

This is deliberately a STANDALONE script meant to run persistently on the
always-up Linux server (systemd timer or a screen/tmux loop), NOT an agent
background task. It checks far more often than a human and survives restarts
because all state lives in the server-side stats:transcribe key.

Auth: reads the API key from --token-file (default ./.api-token, format
"api_key=abk_..."). The server speaks TLS on :8484; --insecure skips cert
verification (self-signed prod cert).

Examples:
  # one-shot check (cron/systemd-timer friendly)
  python3 transcribe_monitor.py --once

  # continuous, poll every 30s, alert + auto-relaunch when idle
  python3 transcribe_monitor.py --interval 30 --relaunch

  # against a specific server explicitly
  python3 transcribe_monitor.py --base https://<server>:8484 --insecure

The API base URL is required: pass --base or set the ABK_API_URL environment
variable (ABK_BASE is also honored for backward compatibility).
"""

import argparse
import json
import os
import ssl
import sys
import time
import urllib.error
import urllib.parse
import urllib.request
from datetime import datetime, timezone


def log_line(metrics_path, record):
    """Append one JSONL metrics record."""
    if not metrics_path:
        return
    try:
        with open(metrics_path, "a") as f:
            f.write(json.dumps(record) + "\n")
    except OSError as e:
        print(f"WARN: could not write metrics log {metrics_path}: {e}", file=sys.stderr)


def now_iso():
    return datetime.now(timezone.utc).isoformat()


def read_token(token_file):
    """Extract the API key from token_file.

    The .api-token file is multi-line (api_key=abk_..., key_id=..., username=...),
    so we scan for the api_key line rather than reading the whole file. Falls back
    to a bare single-line token if no api_key= line is present.
    """
    try:
        with open(token_file) as f:
            lines = [ln.strip() for ln in f if ln.strip()]
    except OSError as e:
        print(f"FATAL: cannot read token file {token_file}: {e}", file=sys.stderr)
        sys.exit(2)
    for ln in lines:
        if ln.startswith("api_key="):
            return ln.split("=", 1)[1].strip()
    # No api_key= line: treat a single bare line as the token.
    if len(lines) == 1 and "=" not in lines[0]:
        return lines[0]
    print(f"FATAL: no api_key= line found in {token_file}", file=sys.stderr)
    sys.exit(2)


def http_get(url, token, insecure, timeout=15):
    req = urllib.request.Request(url, headers={"Authorization": f"Bearer {token}"})
    ctx = ssl._create_unverified_context() if insecure else None
    with urllib.request.urlopen(req, timeout=timeout, context=ctx) as resp:
        return json.loads(resp.read().decode())


def http_post(url, token, insecure, body, timeout=15):
    data = json.dumps(body).encode()
    req = urllib.request.Request(
        url,
        data=data,
        headers={"Authorization": f"Bearer {token}", "Content-Type": "application/json"},
        method="POST",
    )
    ctx = ssl._create_unverified_context() if insecure else None
    with urllib.request.urlopen(req, timeout=timeout, context=ctx) as resp:
        return json.loads(resp.read().decode())


def whisper_alive(whisper_url, timeout=5):
    if not whisper_url:
        return None  # not checked
    try:
        with urllib.request.urlopen(whisper_url.rstrip("/") + "/health", timeout=timeout) as resp:
            body = json.loads(resp.read().decode())
            return body.get("status") == "ok"
    except Exception:
        return False


def fetch_stats(base, token, insecure):
    """Return the stats dict, or None when the server reports no run yet."""
    url = base.rstrip("/") + "/api/v1/maintenance/transcribe-stats"
    body = http_get(url, token, insecure)
    # RespondWithOK wraps the payload as {"data": <stats|null>}.
    return body.get("data") if isinstance(body, dict) else body


def relaunch_op(base, token, insecure):
    url = base.rstrip("/") + "/api/v1/operations/v2"
    return http_post(
        url,
        token,
        insecure,
        {
            "def_id": "maintenance.transcribe-book-intros",
            # The op previews unless told otherwise (dry_run defaults to true): a
            # relaunch that omitted this would queue a run that transcribes nothing.
            "params": {"dry_run": False},
        },
    )


# A preview run of transcribe-book-intros says so in its progress message
# (previewIntroTranscribe in internal/plugins/maintenance/intro_transcribe.go).
# Every progress message of a transcribe-book-intros run opens with its mode
# (introPreviewMarker / introLiveMarker in
# internal/plugins/maintenance/intro_transcribe.go, pinned by
# TestIntroTranscribe_ProgressNamesTheMode), including the "Selecting work"
# line both modes report before they diverge. Only these markers decide.
PREVIEW_MARKER = "PREVIEW (dry_run, nothing written) — "
LIVE_MARKER = "LIVE — "
TERMINAL_STATUSES = {"completed", "failed", "canceled", "cancelled", "interrupted", "abandoned"}


def fetch_op(base, token, insecure, op_id):
    """Return the v2 operation view for op_id (status, progress_message, ...)."""
    url = base.rstrip("/") + "/api/v1/operations/v2/" + urllib.parse.quote(op_id, safe="")
    body = http_get(url, token, insecure)
    data = body.get("data", body) if isinstance(body, dict) else {}
    return (data or {}).get("operation") or {}


def classify_relaunch(op):
    """'preview', 'live' or 'unknown' for a relaunched op's v2 view.

    Decided only by the mode marker its progress message opens with. Anything
    else (no message yet, an error, a server build without the markers) is
    unknown: a message without a marker is not evidence of a live run.
    """
    msg = op.get("progress_message") or ""
    if msg.startswith(PREVIEW_MARKER):
        return "preview"
    if msg.startswith(LIVE_MARKER):
        return "live"
    return "unknown"


def preview_banner(op_id, msg, alerts_path):
    """The loud form: an ERROR alert plus a banner nobody scrolls past."""
    emit_alert(
        "ERROR",
        "RELAUNCH_IS_PREVIEW",
        f"relaunched op {op_id} is a PREVIEW and transcribes nothing: {msg!r}. "
        "The server ignored dry_run=false or answered with an active preview run; "
        "check the deployed build and relaunch with dry_run=false by hand.",
        alerts_path,
    )
    bar = "!" * 78
    print(
        f"{bar}\n!! RELAUNCH CAME BACK AS A PREVIEW: op {op_id}\n!! {msg}\n{bar}",
        file=sys.stderr,
        flush=True,
    )


def check_relaunch(args, token, state, record, wait_secs=0):
    """Classify the pending relaunch, waiting up to wait_secs for it to report.

    Resolved (preview or live) clears state['pending_relaunch']. Unknown keeps it
    for the next poll, unless the run has already settled (it will never report
    a marker now) or --relaunch-verify-max has passed: then RELAUNCH_UNVERIFIED.
    Returns the classification.
    """
    pending = state.get("pending_relaunch")
    if not pending:
        return None
    op_id = pending["op_id"]
    deadline = time.time() + wait_secs
    while True:
        try:
            op = fetch_op(args.base, token, args.insecure, op_id)
        except (urllib.error.URLError, urllib.error.HTTPError, ValueError, TimeoutError) as e:
            record["relaunch_check"] = f"fetch failed: {e}"
            return "unknown"
        kind = classify_relaunch(op)
        if kind != "unknown" or time.time() >= deadline:
            break
        time.sleep(2)
    record["relaunch_check"] = kind
    if kind == "preview":
        state.pop("pending_relaunch", None)
        preview_banner(op_id, op.get("progress_message") or "", args.alerts_log)
    elif kind == "live":
        state.pop("pending_relaunch", None)
        emit_alert(
            "INFO",
            "RELAUNCH_LIVE",
            f"relaunched op {op_id} is running live (status {op.get('status')!r})",
            args.alerts_log,
        )
    else:
        status = (op.get("status") or "").lower()
        settled = status in TERMINAL_STATUSES
        if settled or time.time() - pending["ts"] >= args.relaunch_verify_max:
            state.pop("pending_relaunch", None)
            emit_alert(
                "WARN",
                "RELAUNCH_UNVERIFIED",
                f"relaunched op {op_id} (status {status!r}, after "
                f"{int(time.time() - pending['ts'])}s) reported no mode marker: "
                f"{(op.get('progress_message') or '')!r}; cannot tell whether it is a preview",
                args.alerts_log,
            )
    return kind


def summarize(stats):
    """One-line human summary of a stats dict."""
    if not stats:
        return "no run has produced stats yet"
    s = stats
    return (
        f"attempted={s.get('attempted', 0)} ok={s.get('ok', 0)} "
        f"src_missing={s.get('source_file_missing', 0)} "
        f"ffmpeg_err={s.get('ffmpeg_error', 0)} whisper_err={s.get('whisper_error', 0)} "
        f"path_unmapped={s.get('path_unmapped', 0)} "
        f"no_audio={s.get('no_audio', 0)} empty={s.get('empty', 0)} "
        f"skipped={s.get('skipped_existing', 0)} cache_hits={s.get('cache_hits', 0)} "
        f"done={s.get('done', False)} total_books={s.get('total_books', 0)}"
    )


def progress_key(stats):
    """A scalar that increases while a run makes progress, for stall detection."""
    if not stats:
        return -1
    return sum(
        stats.get(k, 0)
        for k in (
            "attempted",
            "ok",
            "source_file_missing",
            "ffmpeg_error",
            "whisper_error",
            "path_unmapped",
            "no_audio",
            "empty",
            "skipped_existing",
        )
    )


def emit_alert(level, code, msg, alerts_path):
    line = f"{now_iso()} ALERT[{level}] {code}: {msg}"
    print(line, flush=True)
    if alerts_path:
        try:
            with open(alerts_path, "a") as f:
                f.write(line + "\n")
        except OSError as e:
            print(f"WARN: could not write alerts log {alerts_path}: {e}", file=sys.stderr)


def run_once(args, token, state):
    """Single poll. Mutates `state` (dict) for cross-poll stall tracking.
    Returns the metrics record."""
    ts = now_iso()
    record: dict = {"ts": ts}

    # A relaunch from an earlier poll that had not reported yet.
    check_relaunch(args, token, state, record)

    # Whisper health (optional).
    w = whisper_alive(args.whisper_url)
    record["whisper_ok"] = w
    if w is False:
        emit_alert("ERROR", "WHISPER_DOWN", f"{args.whisper_url}/health not ok", args.alerts_log)

    # Stats.
    try:
        stats = fetch_stats(args.base, token, args.insecure)
    except (urllib.error.URLError, urllib.error.HTTPError, ValueError, TimeoutError) as e:
        emit_alert(
            "ERROR", "API_UNREACHABLE", f"transcribe-stats fetch failed: {e}", args.alerts_log
        )
        record["error"] = str(e)
        log_line(args.metrics_log, record)
        return record

    record["stats"] = stats
    print(f"{ts}  {summarize(stats)}", flush=True)

    # --- alerting ---------------------------------------------------------
    if not stats:
        emit_alert("WARN", "IDLE", "no transcription run has ever produced stats", args.alerts_log)
        if args.relaunch:
            _try_relaunch(args, token, state, record)
        log_line(args.metrics_log, record)
        return record

    done = bool(stats.get("done"))
    ok = stats.get("ok", 0)
    src_missing = stats.get("source_file_missing", 0)
    attempted = stats.get("attempted", 0)
    pk = progress_key(stats)

    if done:
        # Completed run. Flag the did-nothing pattern, then treat as idle.
        if attempted > 0 and ok == 0:
            emit_alert(
                "WARN",
                "NOPROGRESS",
                f"run completed with 0 ok of {attempted} attempted "
                f"({src_missing} source_file_missing) — files likely moved (stale paths)",
                args.alerts_log,
            )
        emit_alert("INFO", "IDLE", "last run is done; no transcription in flight", args.alerts_log)
        if args.relaunch:
            _try_relaunch(args, token, state, record)
    else:
        # In-flight run. Stall detection on the progress key.
        last_pk = state.get("last_pk")
        last_change = state.get("last_change_ts")
        if last_pk is not None and pk == last_pk:
            stalled_for = time.time() - (last_change or time.time())
            record["stalled_for_sec"] = int(stalled_for)
            if stalled_for >= args.stall_secs:
                emit_alert(
                    "ERROR",
                    "STALL",
                    f"counters unchanged for {int(stalled_for)}s (>= {args.stall_secs}s) "
                    f"while run in-flight; op or GPU may be stuck",
                    args.alerts_log,
                )
        else:
            state["last_change_ts"] = time.time()
        state["last_pk"] = pk

    log_line(args.metrics_log, record)
    return record


def _try_relaunch(args, token, state, record):
    """Relaunch the op, respecting a cooldown so we don't spam when every book
    is stale-pathed (which would complete instantly and re-trigger IDLE)."""
    last = state.get("last_relaunch_ts", 0)
    if time.time() - last < args.relaunch_cooldown:
        record["relaunch"] = "cooldown"
        return
    try:
        resp = relaunch_op(args.base, token, args.insecure)
        op_id = resp.get("op_id") if isinstance(resp, dict) else None
        state["last_relaunch_ts"] = time.time()
        record["relaunch"] = op_id or "ok"
        emit_alert("INFO", "RELAUNCH", f"relaunched transcription op {op_id}", args.alerts_log)
    except Exception as e:
        record["relaunch"] = f"failed: {e}"
        emit_alert("ERROR", "RELAUNCH_FAILED", str(e), args.alerts_log)
        return
    if not op_id:
        emit_alert(
            "WARN",
            "RELAUNCH_UNVERIFIED",
            f"relaunch response named no op_id: {resp!r}",
            args.alerts_log,
        )
        return
    # The server decides the mode (and may answer with an already-active run),
    # so confirm what was queued instead of trusting the request body.
    state["pending_relaunch"] = {"op_id": op_id, "ts": time.time()}
    check_relaunch(args, token, state, record, wait_secs=args.relaunch_verify_wait)


def main():
    ap = argparse.ArgumentParser(description="Transcription progress monitor + alerter")
    ap.add_argument(
        "--base",
        default=os.environ.get("ABK_API_URL", os.environ.get("ABK_BASE")),
        help="API base URL, e.g. https://<server>:8484 "
        "(required; defaults to $ABK_API_URL, then $ABK_BASE)",
    )
    ap.add_argument(
        "--token-file",
        default=os.environ.get("ABK_TOKEN_FILE", ".api-token"),
        help="file with 'api_key=abk_...' (default ./.api-token)",
    )
    ap.add_argument(
        "--whisper-url",
        default=os.environ.get("WHISPER_URL", ""),
        help="Whisper server base for /health check, e.g. "
        "http://<whisper-host>:19847 (defaults to $WHISPER_URL; "
        "'' skips the check)",
    )
    ap.add_argument("--interval", type=int, default=30, help="poll interval seconds (default 30)")
    ap.add_argument(
        "--stall-secs",
        type=int,
        default=300,
        help="alert if in-flight counters unchanged this long (default 300)",
    )
    ap.add_argument(
        "--metrics-log",
        default=os.environ.get("ABK_METRICS_LOG", "transcribe-metrics.jsonl"),
        help="JSONL metrics output path ('' to disable)",
    )
    ap.add_argument(
        "--alerts-log",
        default=os.environ.get("ABK_ALERTS_LOG", "transcribe-alerts.log"),
        help="alerts output path ('' to disable)",
    )
    ap.add_argument(
        "--relaunch",
        action="store_true",
        help="auto-relaunch the op when idle (respects --relaunch-cooldown)",
    )
    ap.add_argument(
        "--relaunch-cooldown",
        type=int,
        default=900,
        help="min seconds between auto-relaunches (default 900)",
    )
    ap.add_argument(
        "--relaunch-verify-wait",
        type=int,
        default=30,
        help="seconds to wait right after a relaunch for the op to report "
        "whether it is a preview (default 30; later polls keep checking)",
    )
    ap.add_argument(
        "--relaunch-verify-max",
        type=int,
        default=1800,
        help="give up classifying a relaunch after this many seconds and "
        "raise RELAUNCH_UNVERIFIED (default 1800)",
    )
    ap.add_argument(
        "--insecure",
        action="store_true",
        default=False,
        help="skip TLS cert verification. Prod uses a self-signed cert, so "
        "you must pass --insecure (or add the server CA to the trust store). "
        "Off by default to avoid silently disabling verification.",
    )
    ap.add_argument("--once", action="store_true", help="run a single poll and exit")
    args = ap.parse_args()

    if not args.base:
        ap.error(
            "--base is required (or set the ABK_API_URL environment variable "
            "to your server URL, e.g. https://<server>:8484)"
        )

    token = read_token(args.token_file)
    state = {}

    if args.once:
        run_once(args, token, state)
        return

    print(
        f"{now_iso()} transcribe-monitor starting: base={args.base} interval={args.interval}s "
        f"stall={args.stall_secs}s relaunch={args.relaunch}",
        flush=True,
    )
    while True:
        try:
            run_once(args, token, state)
        except KeyboardInterrupt:
            print("interrupted; exiting", flush=True)
            return
        except Exception as e:  # never let the loop die on an unexpected error
            emit_alert("ERROR", "MONITOR_EXCEPTION", repr(e), args.alerts_log)
        time.sleep(args.interval)


if __name__ == "__main__":
    main()
