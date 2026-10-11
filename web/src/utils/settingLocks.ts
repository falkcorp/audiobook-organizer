// file: web/src/utils/settingLocks.ts
// version: 1.0.0
// guid: 6c1f9e24-8a3d-4b75-9d02-5e7a1c8b3f46
// last-edited: 2026-10-11

/**
 * What GET /api/v1/config reports in `setting_locks` for a key the server's
 * config file locks. Mirrors config.LockedByConfigFile in internal/config; it is
 * not an environment variable name (it has a space) and does not start with `--`.
 */
export const LOCKED_BY_CONFIG_FILE = 'config file';

/**
 * describeLock turns a lock mechanism into the sentence fragment that tells the
 * operator where to look. A flag, an environment variable and the config file
 * live in different places, and sending someone to the wrong one is worse than a
 * disabled control with no explanation at all: they delete something that was
 * never the cause and conclude the app is broken.
 */
export function describeLock(mechanism: string | undefined): string {
  if (!mechanism) return 'the server configuration';
  if (mechanism === LOCKED_BY_CONFIG_FILE) return "the server's config file";
  return mechanism.startsWith('--')
    ? `the ${mechanism} flag on the server's start-up command`
    : `the ${mechanism} environment variable on the server`;
}
