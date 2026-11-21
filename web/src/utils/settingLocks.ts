// file: web/src/utils/settingLocks.ts
// version: 2.0.0
// guid: 6c1f9e24-8a3d-4b75-9d02-5e7a1c8b3f46
// last-edited: 2026-11-19

/**
 * What GET /api/v1/config reports as a key's `locked_by` (and the page keeps in
 * `setting_locks`): which operator layer forces the key's value.
 *
 *   `env ABO_CORE_LOG_LEVEL`   an environment variable
 *   `flag --db`                a command-line flag
 *   `file <name>`              the config file the server read (`<name>` is its path)
 *
 * Mirrors the strings core's config Locks produce.
 */
const ENV_PREFIX = 'env ';
const FLAG_PREFIX = 'flag ';
const FILE_PREFIX = 'file ';

/**
 * describeLock turns a lock mechanism into the sentence fragment that tells the
 * operator where to look. A flag, an environment variable and the config file
 * live in different places, and sending someone to the wrong one is worse than a
 * disabled control with no explanation at all: they delete something that was
 * never the cause and conclude the app is broken.
 */
export function describeLock(mechanism: string | undefined): string {
  if (!mechanism) return 'the server configuration';
  if (mechanism.startsWith(ENV_PREFIX)) {
    return `the ${mechanism.slice(ENV_PREFIX.length)} environment variable on the server`;
  }
  if (mechanism.startsWith(FLAG_PREFIX)) {
    return `the ${mechanism.slice(FLAG_PREFIX.length)} flag on the server's start-up command`;
  }
  if (mechanism === 'file') return "the server's config file";
  if (mechanism.startsWith(FILE_PREFIX)) {
    return `the server's config file (${mechanism.slice(FILE_PREFIX.length)})`;
  }
  // A layer this page does not know by name: say what the server said.
  return mechanism;
}
