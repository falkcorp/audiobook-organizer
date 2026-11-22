<!-- file: docs/configuration.md -->
<!-- version: 1.7.0 -->
<!-- guid: 0ec741a2-f3cf-4a0e-a59f-07cd513eb86b -->
<!-- last-edited: 2026-10-23 -->

# Configuration Reference

Settings live in the core config store. Every key has a dotted path
(`core.http.listen`, `modules.books.scan.on_startup`), a type, a default and an
environment name derived from the path. This page covers how the sources layer,
how a key is locked, how to spell values, and the keys most deployments touch.
The normative description is
[`docs/specs/platform-core/05-config.md`](specs/platform-core/05-config.md)
(section 1.3.5 "Precedence, locks and the boot record" and section 1.3.6 "Key
grammar, env names and retired names").

## Sources and precedence

From highest to lowest, the value a key runs with comes from:

1. **Command-line flags**, only the ones you pass. A flag left at its default
   supplies nothing.
2. **Environment variables** with the `ABO_` prefix.
3. **The operator file**, a YAML file named with `--config`.
4. **The stored value**, set in Settings in the web UI (or by `PUT /api/v1/config`).
5. **The built-in default.**

Flags, environment and the operator file are the **operator layers**. They
always beat the stored value, never the other way round. What you put in the
unit file, the environment or the command line is what runs.

### Locks

A key that any operator layer supplies is **locked**:

- A stored value for it is ignored at load. It is kept, not deleted, and boot
  logs once per boot which key it ignored and which operator source set it.
- A write to it through Settings or `PUT /api/v1/config` is refused with a
  locked error that names the source. `GET /api/v1/config` returns the source in
  `setting_locks`, and Settings shows the field as locked.
- Remove the operator value and restart to unlock it. The stored value is used
  again.

Some keys are never stored at all. `core.storage.path` and `core.paths.*` are
read from the operator layers only, and the ABS JWT secret
(`modules.books.abs.jwt_secret`) and the Cloudflare policy token
(`modules.books.cfguests.policy_api_token`) likewise cannot be saved from the UI.

One exception: **fail-closed keys.** For `core.auth.mode` and
`core.http.basic_gate.enabled` the more restrictive of the operator value and the
stored value wins. A stale `ABO_CORE_AUTH_MODE=disabled` left in a unit cannot
reopen a gate an admin closed in Settings, while an operator can always close
one. When the stored value beats a weaker operator value, boot logs a warning and
health reports `config_fail_closed_override`.

### Where the operator file comes from

Pass `--config <file>`. The file is a YAML mapping nested by key path, with the
top-level keys `core`, `modules` and `integrations`:

```yaml
core:
  log:
    level: info
  http:
    listen: 0.0.0.0:8484
    peer_allowlist: ["192.0.2.0/24"]
modules:
  books:
    scan:
      on_startup: false
    organize:
      folder_naming_pattern: "{author}/{series}/{title} ({print_year})"
integrations:
  download:
    deluge:
      url: http://deluge.example.com:8112
```

There is no default operator file. A file at `$HOME/.audiobook-organizer.yaml`
(or `.yml`) is **not read**, and while one exists and `--config` is not given the
server refuses to start rather than drop its settings. Likewise a `--config` file
that still uses the old flat key names (`root_dir`, `enable_auth` and so on) is
refused. The `config.yaml` that older releases kept next to the database is
imported once when the release first boots; after that nothing reads it. An
unknown top-level key in the operator file produces a warning.

## Environment variables

### Names

A key's environment name is `ABO_` plus the upper-cased path, with `.` and `-`
turned into `_` and the leading `modules.` dropped:

| Key path | Environment name |
|----------|------------------|
| `core.log.level` | `ABO_CORE_LOG_LEVEL` |
| `modules.books.scan.on_startup` | `ABO_BOOKS_SCAN_ON_STARTUP` |
| `integrations.ai.openai.api_key` | `ABO_INTEGRATIONS_AI_OPENAI_API_KEY` |

Names are case-sensitive: only the exact upper-case spelling is read. A variable
set to the empty string counts as unset (it supplies nothing and locks nothing).
To supply an empty value use the operator file.

Any key can also be given as `<NAME>_FILE=<path>`. The file is read once at
start and one trailing newline is trimmed. Use it for secrets from systemd
`LoadCredential=` or container secrets.

Two names from other software are also read, as a second canonical name for
their key: `OPENAI_API_KEY` and `OPENAI_BASE_URL` (for
`integrations.ai.openai.api_key` and `.base_url`), plus the OpenTelemetry
exporter variables (`OTEL_EXPORTER_OTLP_ENDPOINT` and the related names). If both
spellings are set to different values, the `ABO_` name wins with a warning, and
the start is refused if the key is protected at admin level or higher.

An environment variable that starts with `ABO_` and names no key is **ignored
with a warning** (one warning per variable). A typo in a name therefore does not
stop the server, and does not take effect either: check the log.

### Value forms

Values are decoded strictly, for environment, file and flags alike. A value that
does not decode stops the start and the message names the variable.

| Type | Form | Example |
|------|------|---------|
| bool | `true` or `false` only (not `1`, `yes` or `on`) | `ABO_BOOKS_SCAN_ON_STARTUP=true` |
| integer | decimal digits, in the unit the key defines | `ABO_CORE_HTTP_RATE_LIMIT_PER_MINUTE=100` |
| duration | Go duration text | `ABO_BOOKS_ABS_ACCESS_TOKEN_TTL=720h` |
| string, path | the text itself | `ABO_CORE_STORAGE_PATH=/srv/data/audiobooks.pebble` |
| list, object | JSON, in single quotes in a unit or shell | `ABO_CORE_HTTP_PEER_ALLOWLIST='["192.0.2.10","192.0.2.0/24"]'` |

Old spellings are not read: a comma-separated list, a bare number of
nanoseconds for a duration, or `yes`/`1` for a bool.

### Common keys

Sizes are in **bytes**, not megabytes. Integers carry the unit in the key name
(`_bytes`, `_seconds`, `_days`, `_per_minute`).

| Environment name | Type | Default | Notes |
|------------------|------|---------|-------|
| `ABO_CORE_STORAGE_PATH` | path | `<state dir>/storage/main.pebble` | Database location. Operator layers only. Must be absolute (`--db` is made absolute). |
| `ABO_CORE_PATHS_STATE_DIR` | path | built-in state directory | Keyring, boot record and bootstrap state. |
| `ABO_CORE_LIBRARY_ROOTS` | JSON list of root entries | empty (the library root is a stored setting) | See below. |
| `ABO_BOOKS_PLAYLIST_DIR` | path | `playlists` | Playlist output directory. |
| `ABO_INTEGRATIONS_AI_OPENAI_API_KEY` | secret | unset | Also read as `OPENAI_API_KEY`. |
| `ABO_BOOKS_AI_ENABLE_AI_PARSING` | bool | `true` | |
| `ABO_BOOKS_SCAN_CONCURRENT_SCANS` | integer | the larger of the CPU count and 4 | |
| `ABO_CORE_HTTP_LISTEN` | `host:port` | `localhost:8484` | Set by `--host` and `--port` when passed. |
| `ABO_CORE_HTTP_RATE_LIMIT_PER_MINUTE` | integer | `100` | |
| `ABO_CORE_HTTP_BODY_JSON_MAX_BYTES` | integer, bytes | `1048576` (1 MiB) | Was `JSON_BODY_LIMIT_MB`. |
| `ABO_CORE_HTTP_BODY_UPLOAD_MAX_BYTES` | integer, bytes | `10485760` (10 MiB) | Was `UPLOAD_BODY_LIMIT_MB`. |
| `ABO_BOOKS_MASTER_LIST_RETENTION` | integer | `10` | |
| `ABO_CORE_AUTH_MODE` | `required` or `disabled` | `required` | Fail-closed key, see Locks. Was `ENABLE_AUTH=true`/`false`. |
| `ABO_CORE_HTTP_PEER_ALLOWLIST` | JSON list | empty (every peer) | CIDRs or bare addresses. |
| `ABO_CORE_HTTP_TRUSTED_PROXIES` | JSON list | empty | CIDRs or addresses. |

`ABO_CORE_LIBRARY_ROOTS` is a JSON list of library root entries. To pin the
library root from the environment:

```text
ABO_CORE_LIBRARY_ROOTS='[{"id":"books-library","path":"/srv/audiobooks","role":"library","owner":"books"}]'
```

When an operator layer sets it, that root replaces the stored one and the field
is locked in Settings. Without it, the library root is the stored setting you
choose in Settings. The `--dir` flag still names a root for the one-shot commands
(`scan`, `organize`) but is not a config key.

`ABO_CORE_AUTH_MODE=disabled` is refused for an instance a remote caller can reach
unless `core.auth.disabled_allow_remote` is also set.

### Deluge

The Deluge connection is two integration keys and three books keys. They follow
the general rules above, with no special precedence of their own.

| Environment name | Type | Default | Notes |
|------------------|------|---------|-------|
| `ABO_INTEGRATIONS_DOWNLOAD_DELUGE_URL` | URL | unset | Deluge Web UI, for example `http://deluge.example.com:8112`. |
| `ABO_INTEGRATIONS_DOWNLOAD_DELUGE_PASSWORD` | secret | unset | Supports `ABO_INTEGRATIONS_DOWNLOAD_DELUGE_PASSWORD_FILE`. |
| `ABO_BOOKS_DOWNLOAD_DISCOVERY_LABEL` | string | empty | Label discovery looks for, for example `audiobooks`. |
| `ABO_BOOKS_DOWNLOAD_DISCOVERY_ENABLED` | bool | `false` | |
| `ABO_BOOKS_DOWNLOAD_MOVE_ENABLED` | bool | `false` | |

A value from an operator layer locks its key. A write to a locked Deluge key is
refused like any other. The password is a secret: it is write-only, is never
returned by `GET /api/v1/config` (not even masked) and is never logged.

## Retired environment names

Releases before the config flip read unprefixed names (`ROOT_DIR`,
`DATABASE_PATH`, `ENABLE_AUTH`, `DELUGE_WEB_URL` and so on). Those names are
**never read** now. A deployment that still sets one is handled at start:

- if the key it became is protected (sign-in, path, proxy, secret or database
  location), the server **refuses to start** and names every such variable;
- otherwise the value is **ignored with a warning**, and health reports
  `config_retired_name_ignored` while one is set.

Before deploying, run `audiobook-organizer config check`. It opens no store,
reads only the environment and the operator file, and prints each retired name
with its replacement. It exits non-zero when a name would stop the server, or a
value does not decode. Pass the flags the service starts with as
`--set <path>=<value>` (for example `--set core.storage.path=/srv/data/audiobooks.pebble`).

The full list, with the refuse or warn verdict for each name, is
[`deploy/retired-env-names.txt`](../deploy/retired-env-names.txt). The common ones:

| Retired name | Replacement | At start |
|--------------|-------------|----------|
| `ROOT_DIR` | `ABO_CORE_LIBRARY_ROOTS` | refuse |
| `DATABASE_PATH` | `ABO_CORE_STORAGE_PATH` | refuse |
| `DATABASE_TYPE` | none (Pebble is the only backend) | warn |
| `PLAYLIST_DIR` | `ABO_BOOKS_PLAYLIST_DIR` | refuse |
| `ENABLE_AUTH` | `ABO_CORE_AUTH_MODE` (`true` becomes `required`, `false` becomes `disabled`) | refuse |
| `ENABLE_AI_PARSING` | `ABO_BOOKS_AI_ENABLE_AI_PARSING` | warn |
| `CONCURRENT_SCANS` | `ABO_BOOKS_SCAN_CONCURRENT_SCANS` | warn |
| `API_RATE_LIMIT_PER_MINUTE` | `ABO_CORE_HTTP_RATE_LIMIT_PER_MINUTE` | warn |
| `JSON_BODY_LIMIT_MB` | `ABO_CORE_HTTP_BODY_JSON_MAX_BYTES` (megabytes become bytes) | warn |
| `UPLOAD_BODY_LIMIT_MB` | `ABO_CORE_HTTP_BODY_UPLOAD_MAX_BYTES` (megabytes become bytes) | warn |
| `MASTER_LIST_RETENTION` | `ABO_BOOKS_MASTER_LIST_RETENTION` | warn |
| `DELUGE_WEB_URL` | `ABO_INTEGRATIONS_DOWNLOAD_DELUGE_URL` | refuse |
| `DELUGE_WEB_PASSWORD` | `ABO_INTEGRATIONS_DOWNLOAD_DELUGE_PASSWORD` | refuse |
| `DELUGE_DISCOVERY_LABEL` | `ABO_BOOKS_DOWNLOAD_DISCOVERY_LABEL` | warn |
| `DELUGE_DISCOVERY_ENABLED` | `ABO_BOOKS_DOWNLOAD_DISCOVERY_ENABLED` | warn |
| `DELUGE_MOVE_ENABLED` | `ABO_BOOKS_DOWNLOAD_MOVE_ENABLED` | warn |

`OPENAI_API_KEY` is not retired: it is still read, as a second name for
`integrations.ai.openai.api_key`. Convert values as you rename them: bools become
`true` or `false`, lists become JSON, and megabyte sizes become bytes.

## CLI flags

A flag you pass is the highest layer and locks its key. A flag left at its
default does nothing.

### Global flags

| Flag | Description | Default |
|------|-------------|---------|
| `--config` | Operator YAML file (see above) | none |
| `--dir` | Root directory for the one-shot commands (not a config key) | empty |
| `--db` | Database path; sets `core.storage.path` when passed, made absolute | `audiobooks.pebble` |
| `--db-type` | Only `pebble` is accepted | `pebble` |
| `--playlists` | Playlist output directory for the one-shot commands (not a config key) | `playlists` |
| `--log-level` | Log level (`debug`, `info`, `warn`, `error`); sets `core.log.level` when passed | `info` |

The default `--db` is relative to the working directory, and the server refuses
to start if no store path is configured and an `audiobooks.pebble` exists in
the working directory, so it never silently opens a new empty database.

### `serve` flags

| Flag | Description | Default |
|------|-------------|---------|
| `--host`, `--port` | Together set `core.http.listen` when either is passed | `localhost`, `8484` |
| `--read-timeout` | HTTP read timeout | `0s` |
| `--write-timeout` | HTTP write timeout | `0s` |
| `--idle-timeout` | HTTP idle timeout | `120s` |
| `--tls-cert` | TLS certificate path; sets `core.http.tls.cert_file` (with `--tls-key`) | none (plain HTTP) |
| `--tls-key` | TLS private key path; sets `core.http.tls.key_file` | none |
| `--http3-port` | UDP port for HTTP/3 | `8484` |
| `--external-url` | Public origin used in generated links | empty |
| `--workers` | Background operation worker count | `2` |

### `inspect-metadata` flags

| Flag | Description |
|------|-------------|
| `--file` | Audio file path to inspect |

## Setting environment in a deployment

On the systemd deployment, put `Environment=` lines in the `local.conf` drop-in
(`make deploy` renders it from `deploy/local.conf`); see
[`deploy/local.conf.example`](../deploy/local.conf.example) and
[`deploy/audiobook-organizer.service`](../deploy/audiobook-organizer.service).
The deploy helper takes one `KEY=value` per line, with a list value wholly
inside single quotes:

```ini
[Service]
Environment=ABO_BOOKS_SCAN_ON_STARTUP=false
Environment='ABO_CORE_HTTP_PEER_ALLOWLIST=["192.0.2.0/24"]'
```

## Finding other keys

Each module declares its sections with their defaults. The books module's are
in `internal/books/config.go` (`booksSettings`), and core's in
`core/kernel/core_sections.go`. Convert any key's path to its environment name
with the rule above.
