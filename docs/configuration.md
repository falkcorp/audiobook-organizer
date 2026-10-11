<!-- file: docs/configuration.md -->
<!-- version: 1.5.0 -->
<!-- guid: 0ec741a2-f3cf-4a0e-a59f-07cd513eb86b -->
<!-- last-edited: 2026-10-11 -->

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
| `DELUGE_WEB_URL` | `deluge_web_url` | `http://deluge.example.invalid:8112` |
| `DELUGE_WEB_PASSWORD` | `deluge_web_password` | (the Deluge Web UI password) |
| `DELUGE_DISCOVERY_LABEL` | `deluge_discovery_label` | `audiobooks` |
| `DELUGE_DISCOVERY_ENABLED` | `deluge_discovery_enabled` | `true` |
| `DELUGE_MOVE_ENABLED` | `deluge_move_enabled` | `true` |

### Deluge settings precedence

The five Deluge settings above are operator-authoritative. A value from the
environment variable, or from the same key in the config file, **wins over the
value saved in the database** (including a saved empty value), and Settings shows
the field read-only with a note naming the source. The environment wins over the
config file when both are set. An empty variable or an empty config-file value
does not count as set. When neither source sets a key, the saved value (set with
`PUT /api/v1/config`) is used, as for every other setting.

"The config file" here means the file the server was started with: the `--config`
flag, by default `$HOME/.audiobook-organizer.yaml`. The Settings note and the
`setting_locks` value from `GET /api/v1/config` give its path. It is **not** the
`config.yaml` the server keeps next to the database; that file never locks a
setting. If you point `--config` at the `config.yaml` next to the database, the
server rewrites that file when settings are saved. It keeps the keys it does not
manage (the Deluge keys among them), updates the ones it does, removes a secret
that has become empty, and removes any retired setting. Keys are matched without
regard to case, so `ROOT_DIR` is replaced by `root_dir`. The rewrite does not keep
comments and writes the keys in sorted order. It is written to a temporary file and
renamed over the original, keeping the file's permissions (and its owner and group
when the server is allowed to set them; ACLs and extended attributes are not carried
over). If the server can write the file but not its directory, the file is written in
place instead, which keeps its owner but is not atomic, and a warning says so. A
symlinked config file is followed; one whose target does not exist is left alone and
the save reports an error. If the file exists but
cannot be read or is not valid YAML, the server leaves it exactly as it is and logs
a warning naming it; the settings are still saved to the database.

A Deluge boolean that does not parse (the accepted spellings are those of Go's
`strconv.ParseBool`: `1`, `t`, `true`, `0`, `f`, `false` and their capitalised forms) is
treated as `false`, and a warning naming the key and its source is logged.

Changing a locked key with `PUT /api/v1/config`:

- For `deluge_web_url`, `deluge_discovery_label`, `deluge_discovery_enabled` and
  `deluge_move_enabled`, the request is accepted and the new value is saved, like
  the other locked settings. It is in effect until the next start, when the
  environment or config-file value is applied again.
- For `deluge_web_password`, the request is accepted and the new value is in effect
  until the next start, but it is **not saved**: the password already stored is kept
  as it was. The environment or file password is never written to the database
  either. If you later remove the environment or file entry, the password you had
  stored before is used again.

The password is returned by `GET /api/v1/config` only masked, and is never logged.

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
