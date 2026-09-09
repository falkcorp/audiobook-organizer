<!-- file: deploy/README.md -->
<!-- version: 1.3.0 -->
<!-- guid: 67014893-53d8-4968-8ba4-2208288e61f2 -->
<!-- last-edited: 2026-09-09 -->

# Audiobook Organizer - Deployment Files

Service configuration files for running audiobook-organizer as a background service on macOS (launchd) and Linux (systemd).

## Quick Start

### macOS (launchd)

```bash
# 1. Edit the plist to set your paths (USERNAME, AUDIOBOOK_ROOT_DIR, DATABASE_PATH)
nano deploy/launchd/com.jdfalk.audiobook-organizer.plist

# 2. Ensure binary is installed
sudo cp audiobook-organizer /usr/local/bin/
sudo chmod 0755 /usr/local/bin/audiobook-organizer

# 3. Install the service
cp deploy/launchd/com.jdfalk.audiobook-organizer.plist ~/Library/LaunchAgents/

# 4. Load the service
launchctl load ~/Library/LaunchAgents/com.jdfalk.audiobook-organizer.plist

# 5. Verify it's running
launchctl list | grep audiobook-organizer

# 6. View logs
tail -f ~/Library/Logs/audiobook-organizer.log
```

### Linux (systemd)

```bash
# 1. Create service user
sudo useradd -r -s /usr/sbin/nologin -d /var/lib/audiobook-organizer audiobook

# 2. Create directories
sudo mkdir -p /var/lib/audiobook-organizer
sudo mkdir -p /var/log/audiobook-organizer
sudo chown audiobook:audiobook /var/lib/audiobook-organizer /var/log/audiobook-organizer

# 3. Install binary
sudo cp audiobook-organizer /usr/local/bin/
sudo chmod 0755 /usr/local/bin/audiobook-organizer

# 4. Install service file
sudo cp deploy/audiobook-organizer.service /etc/systemd/system/

# 5. Reload systemd and enable service
sudo systemctl daemon-reload
sudo systemctl enable --now audiobook-organizer

# 6. Verify it's running
sudo systemctl status audiobook-organizer

# 7. View logs
journalctl -u audiobook-organizer -f
```

## File Permissions

The audiobook service user needs read access to your audiobook library.

**Option A: Add user to media group (simple)**
```bash
sudo usermod -aG media audiobook
```

**Option B: Set ACLs (more control)**
```bash
sudo setfacl -R -m u:audiobook:rX /path/to/audiobooks
```

## Configuration

Both service files can be configured through environment variables:

- `AUDIOBOOK_ROOT_DIR`: Path to audiobook library (e.g., `/path/to/audiobooks`)
- `DATABASE_PATH`: Path to database file (e.g., `/var/lib/audiobook-organizer/audiobooks.pebble`)
- Port: Default is `8484` (configurable via command-line flags)

Edit the respective service file before installation to set these values.

### The database path is authoritative for more than the database

`DATABASE_PATH` (and the `--db` flag) sets where several other things land,
because they are resolved *beside* the database rather than configured
separately:

| Also lives in `dirname($DATABASE_PATH)` | Why it matters |
|---|---|
| `certs/` | TLS key material, if the service is pointed at certs there. |
| `library.bleve` | The search index. A relocation leaves the populated index behind and builds an empty one, so search silently returns nothing. |
| backups | `backup_dir`, when relative, anchors to the database directory. |

#### What deliberately does NOT live beside the database

The **credentials** used to be in that table and were removed from it on
2026-09-09 (PR #3171). The settings encryption key, `.bootstrap-token` and
`.readonly-key` now live in a fixed directory, `/var/lib/audiobook-organizer`,
regardless of where the database is:

| File | Why it is pinned |
|---|---|
| `.encryption_key` | Protects every stored secret. If it is looked for somewhere it is not, the app generates a new one, and the secrets it protected are re-encrypted from the config file where possible and **deleted** where not. |
| `.bootstrap-token` | The documented emergency-access path, including the `sudoers` rule that reads it. Both name a fixed path; a moving target breaks recovery exactly when recovery is needed. |
| `.readonly-key` | Same reasoning, same directory. |

Override with `ABK_STATE_DIR` — for tests and for containers that cannot write
`/var/lib`. If the default is not creatable and no override is set, the app falls
back to the database directory (the pre-2026-09-09 behaviour) and logs a warning
naming both paths, so `serve` still runs on a developer machine.

**If you move the database, move `.encryption_key` with it — once.** The app
reads the old location as a fallback and logs where to move the file, so nothing
breaks in the meantime. It will not copy the file for you: one secret in two
places is a second place it can leak from.


As of 2026-09-09 an explicitly supplied `DATABASE_PATH` or `--db` **overrides**
the path stored in the config blob (PR #3168). Before that the stored value won,
so relocating the database required editing the database you were moving.

**If you put the database inside the library tree** — which is reasonable, and is
what the prod host does at `<root_dir>/.appdata` — the directory must be
excluded from library scans. That exclusion is a rule, not a naming convention:
`appdirs.FromConfig` reports the database directory to every walker, so it holds
whether or not the directory name begins with a dot. Do not rely on the dot.

## Files Overview

| File | Platform | Purpose |
|------|----------|---------|
| `launchd/com.jdfalk.audiobook-organizer.plist` | macOS | User-level launchd service (recommended) |
| `audiobook-organizer.service` | Linux | systemd service unit (recommended; this is the one `Makefile.local`'s `deploy`/`deploy-debug` targets ship) |
| `systemd/audiobook-organizer.service` | Linux | Symlink to `../audiobook-organizer.service` — kept for the historical `deploy/systemd/...` path; do not edit independently |
| `com.audiobook-organizer.plist` | macOS | Legacy compatibility (use launchd subdirectory) |

## Management Commands

### macOS
```bash
# Start/stop
launchctl start com.jdfalk.audiobook-organizer
launchctl stop com.jdfalk.audiobook-organizer

# Unload (disable startup)
launchctl unload ~/Library/LaunchAgents/com.jdfalk.audiobook-organizer.plist

# Check status
launchctl list | grep audiobook-organizer
```

### Linux
```bash
# Start/stop
sudo systemctl start audiobook-organizer
sudo systemctl stop audiobook-organizer

# Enable/disable startup
sudo systemctl enable audiobook-organizer
sudo systemctl disable audiobook-organizer

# Check status
sudo systemctl status audiobook-organizer

# View logs
journalctl -u audiobook-organizer -f
```

## Troubleshooting

### macOS

**Service won't start:**
- Check permissions: `ls -la ~/Library/LaunchAgents/com.jdfalk.audiobook-organizer.plist`
- Verify binary exists: `which audiobook-organizer`
- Check syntax: `plutil -lint ~/Library/LaunchAgents/com.jdfalk.audiobook-organizer.plist`

**Logs are empty:**
- Verify log paths exist: `ls -la ~/Library/Logs/`
- Check that audiobook-organizer binary is executable: `file /usr/local/bin/audiobook-organizer`

### Linux

**Service won't start:**
- Check service status: `sudo systemctl status audiobook-organizer`
- Check logs: `journalctl -u audiobook-organizer -n 20`
- Verify binary: `ls -la /usr/local/bin/audiobook-organizer`

**Permission issues:**
- Verify user exists: `id audiobook`
- Check directory ownership: `ls -la /var/lib/audiobook-organizer`
- Test file access: `sudo -u audiobook ls /path/to/audiobooks`

## Default Ports

The service runs on **port 8484** by default. Access the web UI at:
- `http://localhost:8484` (local)
- `http://<your-ip>:8484` (from another machine)

Modify the `--port` flag in the service file to use a different port.

## Security Notes

### macOS
- Service runs as the logged-in user
- File creation restricted to user-only (Umask 0077)
- Logs stored in user's Library directory

### Linux
- Service runs as dedicated `audiobook` user (non-root)
- Security hardening enabled:
  - `NoNewPrivileges=yes` - Cannot gain elevated privileges
  - `ProtectKernelTunables=yes` - Cannot modify kernel parameters
  - `ProtectControlGroups=yes` - Cannot modify control groups
  - `PrivateTmp=yes` - Isolated temporary directory
- Logs sent to systemd journal
- Minimal file system access to improve security posture

## Building the Binary

Before deploying, build the binary with:

```bash
make build               # Full build with embedded frontend
make build-api          # Backend only (faster)
```

The binary will be created as `./audiobook-organizer` in the project root.
