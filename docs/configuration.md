<!-- file: docs/configuration.md -->
<!-- version: 1.1.0 -->
<!-- guid: 0ec741a2-f3cf-4a0e-a59f-07cd513eb86b -->
<!-- last-edited: 2026-09-11 -->

# Configuration Reference

This document lists runtime configuration sources for `audiobook-organizer`:

1. Command-line flags
2. Environment variables
3. Config file (`$HOME/.audiobook-organizer.yaml` by default)
4. Built-in defaults

At server startup, persisted database settings are loaded and then environment
overrides are applied again for selected keys (`root_dir`, `openai_api_key`,
`enable_ai_parsing`).

## CLI Flags

### Global Flags

| Flag | Description | Default |
|------|-------------|---------|
| `--config` | Path to config file | `$HOME/.audiobook-organizer.yaml` |
| `--dir` | Root organized-library directory | empty |
| `--db` | Database path | `audiobooks.pebble` |
| `--db-type` | Database backend (`pebble` or `sqlite`) | `pebble` |
| `--playlists` | Playlist output directory | `playlists` |

### `serve` Flags

| Flag | Description | Default |
|------|-------------|---------|
| `--host` | Bind host | `localhost` |
| `--port` | HTTP/HTTPS listen port | `8484` |
| `--read-timeout` | HTTP read timeout | `0s` |
| `--write-timeout` | HTTP write timeout | `0s` |
| `--idle-timeout` | HTTP idle timeout | `120s` |
| `--tls-cert` | TLS certificate path | `certs/localhost.crt` |
| `--tls-key` | TLS private key path | `certs/localhost.key` |
| `--http3-port` | UDP port for HTTP/3 | `8484` |
| `--workers` | Background operation worker count | `2` |

### `inspect-metadata` Flags

| Flag | Description |
|------|-------------|
| `--file` | Audio file path to inspect |

## Environment Variables

Environment variables are read by Viper. Use uppercase config keys (examples
below):

| Variable | Maps to | Example |
|----------|---------|---------|
| `ROOT_DIR` | `root_dir` | `/srv/audiobooks` |
| `DATABASE_PATH` | `database_path` | `/srv/data/audiobooks.pebble` |
| `DATABASE_TYPE` | `database_type` | `pebble` |
| `PLAYLIST_DIR` | `playlist_dir` | `/srv/playlists` |
| `OPENAI_API_KEY` | `openai_api_key` | `sk-...` |
| `ENABLE_AI_PARSING` | `enable_ai_parsing` | `true` |
| `CONCURRENT_SCANS` | `concurrent_scans` | `4` |
| `API_RATE_LIMIT_PER_MINUTE` | `api_rate_limit_per_minute` | `100` |
| `AUTH_RATE_LIMIT_PER_MINUTE` | `auth_rate_limit_per_minute` | `10` |
| `JSON_BODY_LIMIT_MB` | `json_body_limit_mb` | `1` |
| `UPLOAD_BODY_LIMIT_MB` | `upload_body_limit_mb` | `10` |
| `ENABLE_AUTH` | `enable_auth` | `true` |

## Config File Keys

Example:

```yaml
root_dir: /srv/audiobooks
database_path: /srv/data/audiobooks.pebble
database_type: pebble
playlist_dir: /srv/playlists

organization_strategy: auto
scan_on_startup: false
auto_organize: true
folder_naming_pattern: "{author}/{series}/{title} ({print_year})"
file_naming_pattern: "{title} - {author} - read by {narrator}"

enable_auth: true
api_rate_limit_per_minute: 100
auth_rate_limit_per_minute: 10
json_body_limit_mb: 1
upload_body_limit_mb: 10

enable_ai_parsing: false
openai_api_key: ""
```

For the complete set of persisted keys, see `internal/config/config.go` and
`internal/config/persistence.go`.
