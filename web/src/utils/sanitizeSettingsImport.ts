// file: web/src/utils/sanitizeSettingsImport.ts
// version: 1.10.0
// guid: 3f6b1c2e-8d4a-4e7f-9a51-c2d0b7e4f813
// last-edited: 2026-11-19

import type * as api from '../services/api';

// Deny-by-default: anything not named here is dropped from an imported file.
//
// database_path is deliberately absent. A settings file exported from one host
// carries that host's path; importing it would point this host's server at a
// database that does not exist there, and it would start an empty library at
// that path. It is a per-host setting and belongs to the host, not to a
// portable settings blob.
//
// env_locked, setting_locks and secret_status are absent because they are
// server-computed and read-only; they appear in a GET response and are not
// settings at all.
//
// Secrets are never imported. The server does not return them (a secret has a
// status, not a value), so a file this app exported has none, and one that
// carries one was written by hand or by an older version, whose exports held a
// masked placeholder that would overwrite the stored secret with garbage. They
// are named in the report instead, so the user enters them in Settings. An
// older export may still carry activity_db_path, activity_db_move_on_change or
// activity_db_resolved_path from before the SQLite activity backend was
// removed (2026-10-19); the server now refuses the first two, so they are
// dropped here like any other unknown key.
// The top-level secret settings: write-only, so a settings file never carries
// one into the server.
const SECRET_KEYS = ['openai_api_key', 'acoustid_api_key'];

// Keys an older export carries inside a nested section that the server no longer
// has: auto_update's window was read and ignored since the updater hotfix, and
// the server refuses a patch that names a key it does not have.
const RETIRED_NESTED_KEYS: Record<string, string[]> = {
  auto_update: ['window_start', 'window_end'],
};

const withoutRetiredKeys = (section: string, obj: Record<string, unknown>) => {
  const retired = RETIRED_NESTED_KEYS[section] ?? [];
  return Object.fromEntries(Object.entries(obj).filter(([k]) => !retired.includes(k)));
};

export interface SanitizeReport {
  cleaned: Partial<api.Config>;
  // Names of the secret fields in the file that were not imported.
  skipped: string[];
  // Server addresses in the file that were not imported: an address a stored
  // credential is sent to may be changed only by hand, never by a file.
  ignored: string[];
}

/** What the report says of an address the import would have changed. */
export const IGNORED_ADDRESS_NOTE = 'ignored: server address changes must be made by hand';

// An address a stored secret travels to. URLs are never imported: a file that
// set one would point stored credentials at whatever host it names.
//  - metadata_sources[].base_url is dropped on every source, new or existing
//    (a built-in provider takes its key from the global settings, so a new source
//    named like one would still send it there); a source the server already has
//    keeps the base_url it has;
//  - embedding.base_url (the embeddings client sends the OpenAI key there) keeps
//    the server's value.
// Without the server's current config no saved value can be kept, and none from
// the file is imported.
interface CurrentAddresses {
  metadata_sources?: Array<{ id?: string; base_url?: string }>;
  embedding?: { base_url?: string };
}

export const sanitizeSettingsImport = (
  payload: Partial<api.Config>,
  current?: CurrentAddresses
): Partial<api.Config> => sanitizeSettingsImportWithReport(payload, current).cleaned;

export const sanitizeSettingsImportWithReport = (
  payload: Partial<api.Config>,
  current?: CurrentAddresses
): SanitizeReport => {
  const skipped: string[] = [];
  const ignored: string[] = [];
  const allowed = new Set([
    'root_dir',
    'playlist_dir',
    'organization_strategy',
    'scan_on_startup',
    'auto_organize',
    'folder_naming_pattern',
    'file_naming_pattern',
    'create_backups',
    'supported_extensions',
    'exclude_patterns',
    'enable_disk_quota',
    'disk_quota_percent',
    'enable_user_quotas',
    'enable_ai_parsing',
    'metadata_sources',
    'concurrent_scans',
    'cache_invalidate_on_book_update',
    'metadata_fetch_cache_ttl_days',
    'purge_soft_deleted_after_days',
    'backup_retention_days',
    'purge_soft_deleted_delete_files',
    'log_level',
    'auto_rename_on_apply',
    'auto_write_tags_on_apply',
    'verify_after_write',
    'protected_paths',
    // nested sub-struct keys (CFG-1)
    'embedding',
    'dedup',
    'metadata_scoring',
    'itunes',
    'maintenance',
    'scheduled',
    'auto_update',
    'tools',
  ]);

  const cleaned: Partial<api.Config> = {};
  if (!payload || typeof payload !== 'object') return { cleaned, skipped, ignored };

  for (const key of SECRET_KEYS) {
    if (key in payload) skipped.push(key);
  }

  for (const key of Object.keys(payload)) {
    if (!allowed.has(key)) continue;
    // eslint-disable-next-line @typescript-eslint/no-explicit-any
    const val = (payload as any)[key];

    switch (key) {
      case 'root_dir':
      case 'playlist_dir':
      case 'organization_strategy':
      case 'folder_naming_pattern':
      case 'file_naming_pattern':
      case 'log_level':
      case 'protected_paths':
        // eslint-disable-next-line @typescript-eslint/no-explicit-any
        if (typeof val === 'string') (cleaned as any)[key] = val;
        break;

      case 'supported_extensions':
      case 'exclude_patterns':
        // eslint-disable-next-line @typescript-eslint/no-explicit-any
        if (Array.isArray(val)) (cleaned as any)[key] = val.filter((x) => typeof x === 'string');
        break;

      case 'metadata_sources':
        if (Array.isArray(val)) {
          // eslint-disable-next-line @typescript-eslint/no-explicit-any
          const sanitizedSources = (val as any[])
            .map((s) => {
              if (!s || typeof s !== 'object') return null;
              // eslint-disable-next-line @typescript-eslint/no-explicit-any
              const src: any = {};
              // eslint-disable-next-line @typescript-eslint/no-explicit-any
              if (typeof (s as any).id === 'string') src.id = (s as any).id;
              // eslint-disable-next-line @typescript-eslint/no-explicit-any
              if (typeof (s as any).name === 'string') src.name = (s as any).name;
              // eslint-disable-next-line @typescript-eslint/no-explicit-any
              src.enabled = Boolean((s as any).enabled);
              // eslint-disable-next-line @typescript-eslint/no-explicit-any
              src.priority = typeof (s as any).priority === 'number' ? (s as any).priority : 0;
              // eslint-disable-next-line @typescript-eslint/no-explicit-any
              src.requires_auth = Boolean((s as any).requires_auth ?? (s as any).requiresAuth);
              // rate_limit rides along: a save names the whole list, so leaving it
              // out would erase it on the server. base_url is never imported: it is
              // an address a source's credentials are sent to (a built-in provider
              // takes its key from the global settings, so even a source new to the
              // server would send it there). A source the server already has keeps
              // its own base_url, which has to be named because the save names the
              // whole list.
              // eslint-disable-next-line @typescript-eslint/no-explicit-any
              const rl = (s as any).rate_limit;
              if (rl && typeof rl === 'object' && !Array.isArray(rl)) src.rate_limit = rl;
              // eslint-disable-next-line @typescript-eslint/no-explicit-any
              const fileBase =
                typeof (s as any).base_url === 'string' ? (s as any).base_url : undefined;
              const existing = current?.metadata_sources?.find((c) => c.id === src.id);
              if (existing?.base_url) src.base_url = existing.base_url;
              if (fileBase && fileBase !== (existing?.base_url ?? '')) {
                ignored.push(
                  `metadata_sources[${src.id ?? '?'}].base_url (${IGNORED_ADDRESS_NOTE})`
                );
              }
              src.credentials = {};
              // eslint-disable-next-line @typescript-eslint/no-explicit-any
              if ((s as any).credentials && typeof (s as any).credentials === 'object') {
                // eslint-disable-next-line @typescript-eslint/no-explicit-any
                for (const [ck, cv] of Object.entries((s as any).credentials)) {
                  // An exported file carries the keys with blank values; only a
                  // value that was actually in the file is worth reporting.
                  if (typeof cv !== 'string' || cv === '') continue;
                  // Every credential field of a source is a secret in the secret
                  // store, and a file may carry one in any of them: none is imported.
                  // An empty credentials map leaves the saved ones as they are.
                  skipped.push(`metadata_sources[${src.id ?? '?'}].credentials.${ck}`);
                }
              }
              return src;
            })
            .filter(Boolean);
          // eslint-disable-next-line @typescript-eslint/no-explicit-any
          (cleaned as any)[key] = sanitizedSources;
        }
        break;

      // boolean flags
      case 'scan_on_startup':
      case 'auto_organize':
      case 'create_backups':
      case 'enable_disk_quota':
      case 'enable_user_quotas':
      case 'enable_ai_parsing':
      case 'cache_invalidate_on_book_update':
      case 'purge_soft_deleted_delete_files':
      case 'auto_rename_on_apply':
      case 'auto_write_tags_on_apply':
      case 'verify_after_write':
        // eslint-disable-next-line @typescript-eslint/no-explicit-any
        (cleaned as any)[key] = Boolean(val);
        break;

      // numeric fields
      case 'disk_quota_percent':
      case 'concurrent_scans':
      case 'metadata_fetch_cache_ttl_days':
      case 'purge_soft_deleted_after_days':
      case 'backup_retention_days':
        if (typeof val === 'number') {
          // eslint-disable-next-line @typescript-eslint/no-explicit-any
          (cleaned as any)[key] = val;
        } else if (typeof val === 'string' && val.trim() !== '' && !isNaN(Number(val))) {
          // eslint-disable-next-line @typescript-eslint/no-explicit-any
          (cleaned as any)[key] = Number(val);
        }
        break;

      // nested sub-struct objects — pass through as-is (backend validates shape)
      case 'embedding':
      case 'dedup':
      case 'metadata_scoring':
      case 'itunes':
      case 'maintenance':
      case 'scheduled':
      case 'auto_update':
      case 'tools':
        if (val !== null && typeof val === 'object' && !Array.isArray(val)) {
          const section = withoutRetiredKeys(key, val as Record<string, unknown>);
          if (key === 'embedding' && 'base_url' in section) {
            // The embeddings client sends the OpenAI key to this address.
            const keep = current?.embedding?.base_url ?? '';
            if (section.base_url !== keep) {
              ignored.push(`embedding.base_url (${IGNORED_ADDRESS_NOTE})`);
            }
            delete section.base_url;
          }
          // eslint-disable-next-line @typescript-eslint/no-explicit-any
          (cleaned as any)[key] = section;
        }
        break;

      default:
        break;
    }
  }

  // Settings files exported before auto_update and maintenance became nested
  // objects carry flat auto_update_* / maintenance_window_* keys. The server
  // never read those from a PUT, and it now rejects unknown keys with a 400,
  // so fold them into the nested objects here. A file that already has the
  // nested object keeps it as-is.
  const legacy = payload as Record<string, unknown>;
  const legacyNumber = (key: string): number | undefined => {
    const v = legacy[key];
    if (typeof v === 'number') return v;
    if (typeof v === 'string' && v.trim() !== '' && !isNaN(Number(v))) return Number(v);
    return undefined;
  };
  const legacyBool = (key: string): boolean | undefined =>
    key in legacy ? Boolean(legacy[key]) : undefined;
  const pruned = <T extends object>(obj: T): T | undefined => {
    const entries = Object.entries(obj).filter(([, v]) => v !== undefined);
    return entries.length > 0 ? (Object.fromEntries(entries) as T) : undefined;
  };
  if (!cleaned.auto_update) {
    const autoUpdate = pruned({
      enabled: legacyBool('auto_update_enabled'),
      channel:
        typeof legacy.auto_update_channel === 'string' ? legacy.auto_update_channel : undefined,
      check_minutes: legacyNumber('auto_update_check_minutes'),
    });
    if (autoUpdate) cleaned.auto_update = autoUpdate as api.Config['auto_update'];
  }
  if (!cleaned.maintenance) {
    const maintenance = pruned({
      enabled: legacyBool('maintenance_window_enabled'),
      window_start: legacyNumber('maintenance_window_start'),
      window_end: legacyNumber('maintenance_window_end'),
    });
    if (maintenance) cleaned.maintenance = maintenance as api.Config['maintenance'];
  }

  return { cleaned, skipped, ignored };
};
