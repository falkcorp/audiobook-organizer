#!/usr/bin/env python3
# file: scripts/setup-prometheus-auth.py
# version: 1.1.0
# guid: 4d90a2f6-13bc-4e58-9071-8c25e6b3f0a7
# last-edited: 2026-08-02

"""Point Prometheus at audiobook-organizer's now-authenticated /metrics.

This runs ON THE SERVER, which has NO git checkout -- deployment ships only the
binary to /usr/local/bin. So copy the file over and run it by absolute path:

    scp scripts/setup-prometheus-auth.py <server>:/home/<user>/
    ssh <server>
    sudo python3 /home/<user>/setup-prometheus-auth.py

It prompts for an API key (Settings -> API keys, `abk_...`), then:

  1. Verifies the key actually works against /metrics BEFORE changing anything.
  2. Installs it at /etc/prometheus/abo.token, mode 0600, owned by prometheus.
  3. Moves the target out of the SHARED file_sd job (see below).
  4. Adds a dedicated `audiobook-organizer` scrape job.
  5. Validates the config with promtool, reloads, and confirms the target is UP.

Why a dedicated job rather than adding auth to the existing one
---------------------------------------------------------------
The target is currently discovered by the `file_sd_https_insecure` job via
/etc/prometheus/targets/https_insecure/audiobook-organizer.json. That job is
SHARED with other targets, and Prometheus applies a job's `authorization` to
every target it scrapes. Adding the key there would send this server's API key
to every other https_insecure endpoint. So the target moves to its own job and
the shared discovery file is disabled.

Safety
------
Nothing is modified until the key is proven to work, the config is backed up
before editing, promtool must accept the result, and BOTH edits (the config and
the moved file_sd entry) are rolled back together if it does not — rolling back
only one would leave the target scraped by neither job. Re-running is safe: existing state is detected and
left alone rather than duplicated.
"""

from __future__ import annotations

import getpass
import grp
import json
import os
import pwd
import re
import shutil
import ssl
import subprocess
import sys
import time
import urllib.error
import urllib.request
from datetime import datetime
from pathlib import Path

PROM_DIR = Path("/etc/prometheus")
PROM_CONFIG = PROM_DIR / "prometheus.yml"
TOKEN_FILE = PROM_DIR / "abo.token"
SHARED_SD_FILE = PROM_DIR / "targets/https_insecure/audiobook-organizer.json"
DISABLED_SD_FILE = PROM_DIR / "targets/audiobook-organizer.json.disabled"

METRICS_URL = "https://localhost:8484/metrics"
PROM_API = "http://localhost:9090"
JOB_NAME = "audiobook-organizer"

JOB_BLOCK = f"""
  # Added by scripts/setup-prometheus-auth.py.
  # /metrics requires a credential (pen-test MED-1 was closed by gating it), and
  # Prometheus supports bearer auth, so it scrapes with an `abk_` API key.
  # credentials_file, not an inline secret: Prometheus re-reads it every scrape,
  # so rotating the key needs no reload and it never lands in this file.
  # Its own job because a job's authorization applies to EVERY target in it --
  # reusing the shared file_sd job would leak this key to unrelated endpoints.
  - job_name: '{JOB_NAME}'
    scheme: https
    tls_config:
      # The origin serves a self-signed certificate, and this scrape is to
      # LOCALHOST -- the same posture the pre-existing file_sd_https_insecure
      # job already used for this target, so this is not a downgrade.
      #
      # Skipping verification does mean the bearer token is presented to
      # whatever answers on localhost:8484. Over loopback that requires an
      # attacker who already has code execution on this host, at which point
      # the token file itself is readable anyway. The clean fix is to give the
      # origin a certificate trusted here and set ca_file instead -- worth doing,
      # but it is a separate change from restoring the scrape.
      insecure_skip_verify: true
    authorization:
      type: Bearer
      credentials_file: {TOKEN_FILE}
    static_configs:
      - targets: ['localhost:8484']
        labels:
          instance: unimatrixzero
"""


def die(msg: str, code: int = 1) -> None:
    print(f"\n  ERROR: {msg}", file=sys.stderr)
    sys.exit(code)


def info(msg: str) -> None:
    print(f"  {msg}")


def probe_metrics(token: str) -> int:
    """Return the HTTP status /metrics gives for this token."""
    # Verification is disabled for the same reason the scrape job disables it:
    # a self-signed cert on localhost. This probe only reads a status code, and
    # it runs before any change is made, so the worst case of a wrong answer is
    # that the script refuses to proceed.
    ctx = ssl.create_default_context()
    ctx.check_hostname = False
    ctx.verify_mode = ssl.CERT_NONE
    req = urllib.request.Request(METRICS_URL, headers={"Authorization": f"Bearer {token}"})
    try:
        with urllib.request.urlopen(req, timeout=15, context=ctx) as resp:
            return resp.status
    except urllib.error.HTTPError as e:
        return e.code
    except Exception as e:  # noqa: BLE001 - any transport failure is fatal here
        die(f"could not reach {METRICS_URL}: {e}\n"
            "  Is audiobook-organizer running on this host?")
        return 0


def _binary_version(binary: str) -> str:
    """Return the bare version a Prometheus-family binary reports, or ""."""
    try:
        out = subprocess.run([binary, "--version"], capture_output=True, text=True, timeout=10)
    except (OSError, subprocess.SubprocessError):
        return ""
    text = (out.stdout or "") + (out.stderr or "")
    m = re.search(r"version (\d+\.\d+\.\d+)", text)
    return m.group(1) if m else ""


def find_promtool() -> tuple[str | None, str]:
    """Resolve the promtool that matches the RUNNING Prometheus.

    🔴 PATH ORDER IS NOT TRUSTWORTHY HERE, and trusting it silently breaks this
    script in the most confusing way possible.

    Observed on this server 2026-08-02: an unpackaged promtool 2.13.1 (built
    2019) sat in /usr/local/bin, ahead of the packaged 2.53.5 in /usr/bin that
    matches the running Prometheus. `shutil.which` returned the 2019 one, which
    does not know the `authorization:` scrape-config field at all -- it was
    added in Prometheus 2.26 -- so it rejected a PERFECTLY VALID config with:

        field authorization not found in type config.plain

    The script then dutifully restored the original and reported failure, so the
    error looked like a bad config rather than a bad validator.

    Strategy: prefer an exact version match with `prometheus --version`, else
    the newest candidate, and say out loud which one was chosen and why. A
    validator NEWER than the server can only be over-permissive (it accepts
    fields the server ignores); an OLDER one produces false rejections like the
    one above, which is the failure worth engineering against.

    Returns (path_or_None, note_to_log).
    """
    seen: list[str] = []
    for directory in os.environ.get("PATH", "").split(os.pathsep) + ["/usr/bin", "/bin", "/usr/local/bin"]:
        if not directory:
            continue
        candidate = os.path.join(directory, "promtool")
        if os.path.isfile(candidate) and os.access(candidate, os.X_OK) and candidate not in seen:
            seen.append(candidate)
    if not seen:
        return None, ""

    server = _binary_version("prometheus")
    versions = {c: _binary_version(c) for c in seen}

    if server:
        for candidate, version in versions.items():
            if version == server:
                note = f"promtool: using {candidate} (v{version}, matches Prometheus)"
                if candidate != seen[0]:
                    note += f" — NOT the first on PATH ({seen[0]} is v{versions[seen[0]] or '?'})"
                return candidate, note

    def sort_key(path: str) -> tuple[int, ...]:
        version = versions.get(path) or "0.0.0"
        return tuple(int(part) for part in version.split("."))

    best = max(seen, key=sort_key)
    note = f"promtool: using {best} (v{versions[best] or '?'})"
    if server:
        note += (f" — no exact match for Prometheus v{server}; "
                 "an older validator can reject a valid config")
    return best, note


def main() -> None:
    print("\n  Prometheus auth setup for audiobook-organizer\n")

    if os.geteuid() != 0:
        die("must run as root:  sudo python3 scripts/setup-prometheus-auth.py")
    if not PROM_CONFIG.is_file():
        die(f"{PROM_CONFIG} not found — is Prometheus installed on this host?")

    try:
        prom_uid = pwd.getpwnam("prometheus").pw_uid
        prom_gid = grp.getgrnam("prometheus").gr_gid
    except KeyError:
        die("no 'prometheus' user/group on this host")
        return

    # ---- 1. Get and VERIFY the key before touching anything ----------------
    print("  Mint a key in the web UI: Settings -> API keys (starts with 'abk_')")
    token = getpass.getpass("  Paste the API key (input hidden): ").strip()
    if not token:
        die("no key entered")
    if not token.startswith("abk_"):
        print("  WARNING: that does not look like an 'abk_...' key — continuing anyway.")

    info("Verifying the key against /metrics ...")
    status = probe_metrics(token)
    if status == 401:
        die("the server rejected that key (401). Check it was copied in full.")
    if status != 200:
        die(f"unexpected status {status} from /metrics — refusing to continue.")
    info("Key works (200). Nothing has been modified yet.\n")

    # ---- 2. Install the token file -----------------------------------------
    TOKEN_FILE.write_text(token)
    os.chmod(TOKEN_FILE, 0o600)
    os.chown(TOKEN_FILE, prom_uid, prom_gid)
    info(f"Wrote {TOKEN_FILE} (0600 prometheus:prometheus)")

    # ---- 3. Disable the shared-job discovery entry -------------------------
    #
    # Tracked so step 4 can UNDO it. Disabling the shared entry only makes sense
    # alongside the dedicated job that replaces it: if validation then rejects the
    # config and only prometheus.yml is rolled back, the target ends up in NEITHER
    # job and is silently scraped by nothing. That happened in production on
    # 2026-08-02 and is strictly worse than either intended end state.
    moved_sd_file = False
    if SHARED_SD_FILE.exists():
        DISABLED_SD_FILE.parent.mkdir(parents=True, exist_ok=True)
        shutil.move(str(SHARED_SD_FILE), str(DISABLED_SD_FILE))
        moved_sd_file = True
        info(f"Moved {SHARED_SD_FILE.name} out of the shared file_sd job")
        info(f"  -> {DISABLED_SD_FILE}")
    else:
        info("Shared file_sd entry already absent — skipping")

    # ---- 4. Add the dedicated job (idempotent) -----------------------------
    config_text = PROM_CONFIG.read_text()
    stamp = datetime.now().strftime("%Y%m%d-%H%M%S")
    backup = PROM_CONFIG.with_suffix(f".yml.bak-{stamp}")

    if f"job_name: '{JOB_NAME}'" in config_text or f'job_name: "{JOB_NAME}"' in config_text:
        info(f"Job '{JOB_NAME}' already present in prometheus.yml — leaving it alone")
    else:
        shutil.copy2(PROM_CONFIG, backup)
        info(f"Backed up config -> {backup}")
        PROM_CONFIG.write_text(config_text.rstrip("\n") + "\n" + JOB_BLOCK)
        info(f"Appended job '{JOB_NAME}'")

        promtool, promtool_note = find_promtool()
        if promtool:
            if promtool_note:
                info(promtool_note)
            check = subprocess.run(
                [promtool, "check", "config", str(PROM_CONFIG)],
                capture_output=True, text=True,
            )
            if check.returncode != 0:
                shutil.copy2(backup, PROM_CONFIG)
                restored = "prometheus.yml restored"
                # Undo step 3 too. Rolling back only the config would leave the
                # target in neither the shared job nor the dedicated one.
                if moved_sd_file and DISABLED_SD_FILE.exists():
                    SHARED_SD_FILE.parent.mkdir(parents=True, exist_ok=True)
                    shutil.move(str(DISABLED_SD_FILE), str(SHARED_SD_FILE))
                    restored += f"; {SHARED_SD_FILE.name} put back in the shared file_sd job"
                die(f"promtool ({promtool}) rejected the new config — ROLLED BACK ({restored}):\n"
                    f"{check.stdout}\n{check.stderr}\n"
                    "If this mentions 'field authorization not found', the validator is\n"
                    "older than the running Prometheus — see find_promtool().")
            info(f"promtool: config valid ({promtool})")
        else:
            info("promtool not found — skipping validation (reload will catch errors)")

    # ---- 5. Reload and confirm the target recovers -------------------------
    info("Reloading Prometheus ...")
    reload_proc = subprocess.run(
        ["systemctl", "reload", "prometheus"], capture_output=True, text=True
    )
    if reload_proc.returncode != 0:
        info("reload failed, trying restart ...")
        reload_proc = subprocess.run(
            ["systemctl", "restart", "prometheus"], capture_output=True, text=True
        )
        if reload_proc.returncode != 0:
            die(f"could not reload or restart Prometheus:\n{reload_proc.stderr}")

    info("Waiting for the first scrape ...")
    deadline = time.time() + 90
    health = "unknown"
    last_error = ""
    while time.time() < deadline:
        time.sleep(5)
        try:
            with urllib.request.urlopen(f"{PROM_API}/api/v1/targets", timeout=10) as resp:
                data = json.load(resp)
        except Exception:  # noqa: BLE001 - Prometheus may still be starting
            continue
        for target in data.get("data", {}).get("activeTargets", []):
            if target.get("labels", {}).get("job") == JOB_NAME:
                health = target.get("health", "unknown")
                last_error = target.get("lastError", "")
                break
        if health == "up":
            break

    print()
    if health == "up":
        print(f"  ✅ Target '{JOB_NAME}' is UP. Metrics are flowing again.")
    else:
        print(f"  ⚠️  Target '{JOB_NAME}' is '{health}'.")
        if last_error:
            print(f"     lastError: {last_error}")
        print("     Check Prometheus -> Status -> Targets. The config backup, if one")
        print("     was made, is alongside prometheus.yml as .yml.bak-<timestamp>.")
        sys.exit(2)


if __name__ == "__main__":
    main()
