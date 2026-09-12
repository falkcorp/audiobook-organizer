// file: web/src/utils/sanitizeSettingsImport.ts
// version: 1.0.0
// guid: 3f6b1c2e-8d4a-4e7f-9a51-c2d0b7e4f813
// last-edited: 2026-09-12

import type * as api from '../services/api';

// Deny-by-default: anything not named here is dropped from an imported file.
//
// activity_db_path and activity_db_move_on_change are deliberately absent. A
// settings file exported from one host carries that host's path, and importing
// it would queue a relocation of a multi-gigabyte database on the next start of
// a machine where that path may not even exist. It is a per-host setting and
// belongs to the host, not to a portable settings blob.
//
// database_path is absent for the same reason, and more so: a settings file
// from another machine would point this host's server at a database that does
// not exist there, and it would start an empty library at that path.
//
// env_locked, setting_locks and activity_db_resolved_path are absent because
// they are server-computed and read-only; they appear in a GET response and
// are not settings at all.
export const sanitizeSettingsImport = (
  payload: Partial<api.Config>
): Partial<api.Config> => {
  const allowed = new Set([
    'root_dir', 'playlist_dir', 'organization_strategy', 'scan_on_startup', 'auto_organize',
    'folder_naming_pattern', 'file_naming_pattern', 'create_backups', 'supported_extensions',
    'exclude_patterns', 'enable_disk_quota', 'disk_quota_percent', 'enable_user_quotas',
    'default_user_quota_gb', 'auto_fetch_metadata', 'enable_ai_parsing',
    'openai_api_key', 'metadata_sources', 'language',
    'concurrent_scans', 'memory_limit_type', 'cache_size', 'cache_invalidate_on_book_update',
    'metadata_fetch_cache_ttl_days', 'memory_limit_percent', 'memory_limit_mb',
    'purge_soft_deleted_after_days', 'purge_soft_deleted_delete_files', 'log_level', 'log_format',
    'enable_json_logging',
    'auto_rename_on_apply', 'auto_write_tags_on_apply', 'verify_after_write', 'protected_paths',
    // nested sub-struct keys (CFG-1)
    'embedding', 'dedup', 'metadata_scoring', 'itunes', 'maintenance', 'scheduled', 'auto_update', 'tools',
  ]);

  const cleaned: Partial<api.Config> = {};
  if (!payload || typeof payload !== 'object') return cleaned;

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
      case 'language':
      case 'memory_limit_type':
      case 'log_level':
      case 'log_format':
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
          const sanitizedSources = (val as any[]).map((s) => {
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
            src.credentials = {};
            // eslint-disable-next-line @typescript-eslint/no-explicit-any
            if ((s as any).credentials && typeof (s as any).credentials === 'object') {
              // eslint-disable-next-line @typescript-eslint/no-explicit-any
              for (const [ck, cv] of Object.entries((s as any).credentials)) {
                if (typeof cv === 'string') src.credentials[ck] = cv;
              }
            }
            return src;
          }).filter(Boolean);
          // eslint-disable-next-line @typescript-eslint/no-explicit-any
          (cleaned as any)[key] = sanitizedSources;
        }
        break;

      case 'openai_api_key':
        if (typeof val === 'string') {
          // eslint-disable-next-line @typescript-eslint/no-explicit-any
          if (!val.includes('***')) (cleaned as any).openai_api_key = val;
        }
        break;

      // boolean flags
      case 'scan_on_startup':
      case 'auto_organize':
      case 'create_backups':
      case 'enable_disk_quota':
      case 'enable_user_quotas':
      case 'auto_fetch_metadata':
      case 'enable_ai_parsing':
        case 'cache_invalidate_on_book_update':
      case 'purge_soft_deleted_delete_files':
      case 'enable_json_logging':
      case 'auto_rename_on_apply':
      case 'auto_write_tags_on_apply':
      case 'verify_after_write':
        // eslint-disable-next-line @typescript-eslint/no-explicit-any
        (cleaned as any)[key] = Boolean(val);
        break;

      // numeric fields
      case 'disk_quota_percent':
      case 'default_user_quota_gb':
      case 'concurrent_scans':
      case 'cache_size':
      case 'metadata_fetch_cache_ttl_days':
      case 'memory_limit_percent':
      case 'memory_limit_mb':
      case 'purge_soft_deleted_after_days':
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
          // eslint-disable-next-line @typescript-eslint/no-explicit-any
          (cleaned as any)[key] = val;
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
      channel: typeof legacy.auto_update_channel === 'string' ? legacy.auto_update_channel : undefined,
      check_minutes: legacyNumber('auto_update_check_minutes'),
      window_start: legacyNumber('auto_update_window_start'),
      window_end: legacyNumber('auto_update_window_end'),
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

  return cleaned;
};
