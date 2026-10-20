#!/usr/bin/env python3
# file: scripts/org-migration/setup-org-secrets.py
# version: 1.0.1
# guid: a3c7a814-5d73-4440-b5ca-d34db011d85d
# last-edited: 2026-10-20

"""
Phase 5 (partial): Set org-level secrets in falkcorp.
GitHub App secrets (BURNDOWN_BOT_APP_ID, BURNDOWN_BOT_PRIVATE_KEY) must be
set after creating the app via web UI.

Run with:
  BUF_TOKEN=xxx ANTHROPIC_API_KEY=xxx python3 setup-org-secrets.py
"""

import os
import subprocess
import sys

ORG = "falkcorp"


def gh_secret_set(name, value, org=ORG):
    if not value:
        print(f"  SKIP  {name} (no value provided)")
        return False
    result = subprocess.run(
        ["gh", "secret", "set", name, "--org", org, "--visibility", "all"],
        input=value,
        capture_output=True,
        text=True,
    )
    if result.returncode == 0:
        print(f"  OK    {name}")
        return True
    else:
        print(f"  FAIL  {name}: {result.stderr.strip()}")
        return False


secrets = {
    "BUF_TOKEN": os.environ.get("BUF_TOKEN", ""),
    "ANTHROPIC_API_KEY": os.environ.get("ANTHROPIC_API_KEY", ""),
    # Set these after GitHub App creation:
    # "BURNDOWN_BOT_APP_ID": os.environ.get("BURNDOWN_BOT_APP_ID", ""),
    # "BURNDOWN_BOT_PRIVATE_KEY": os.environ.get("BURNDOWN_BOT_PRIVATE_KEY", ""),
}

print(f"Setting org-level secrets in {ORG}...\n")
ok = sum(1 for name, value in secrets.items() if gh_secret_set(name, value))
print(f"\nSet {ok}/{len(secrets)} secrets.")
print("\nRemaining manual steps:")
print("  1. Create GitHub App 'falkcorp-burndown-bot' at:")
print("     https://github.com/organizations/falkcorp/settings/apps/new")
print("     Permissions: contents R/W, pull_requests R/W, issues R/W,")
print("                  workflows R/W, metadata R, checks R")
print("     No events needed (bot polls; no webhooks)")
print("  2. Install app on falkcorp org (all repos)")
print("  3. Get App ID and generate private key (.pem)")
print("  4. Run:")
print(
    "     BURNDOWN_BOT_APP_ID=<id> gh secret set BURNDOWN_BOT_APP_ID --org falkcorp --visibility all"
)
print(
    "     gh secret set BURNDOWN_BOT_PRIVATE_KEY --org falkcorp --visibility all < /path/to/app.pem"
)
print("  5. Set ANTHROPIC_API_KEY if not already done:")
print("     ANTHROPIC_API_KEY=<key> python3 setup-org-secrets.py")
