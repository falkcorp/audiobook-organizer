<!-- file: deploy/README.md -->
<!-- version: 1.1.0 -->
<!-- guid: 67014893-53d8-4968-8ba4-2208288e61f2 -->
<!-- last-edited: 2026-07-18 -->

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
