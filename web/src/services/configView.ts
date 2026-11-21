// file: web/src/services/configView.ts
// version: 1.0.0
// guid: 4b7d2e91-6c35-4a08-b1f4-9e0a3c5d7f26
// last-edited: 2026-11-19

/**
 * GET/PUT /api/v1/config speak in keys: one entry per setting, named by its
 * flat HEAD key, with a class and, for a secret, metadata and never a value.
 * Everything here is the pure translation between that and the nested `Config`
 * object the Settings page edits. The network calls live in api.ts.
 */

/** How the page edits a key: k plain, s secret (write-only), i integration blob, o operator-only. */
export type ConfigKeyClass = 'k' | 's' | 'i' | 'o';

/** What the server's secret store says about a secret. There is never a value. */
export interface ConfigSecretMeta {
  set: boolean;
  /** 'stored' (saved from the UI) or 'env' (supplied by the server's environment). */
  source?: string;
  updated_at?: string;
  fingerprint?: string;
  /** The record exists but cannot be decrypted: it must be entered again. */
  unreadable?: boolean;
}

export interface ConfigKeyView {
  name: string;
  class: ConfigKeyClass;
  /** Absent for a secret. */
  value?: unknown;
  secret?: ConfigSecretMeta;
  /** `env X`, `flag --x` or `file <name>`: what makes the key read-only. */
  locked_by?: string;
  restart_pending?: boolean;
  /** The key needs the admin role (or more) to change. */
  admin?: boolean;
}

export interface ConfigView {
  generation: number;
  keys: ConfigKeyView[];
  naming_pattern_tokens?: string[];
}

/** One secret as the page shows it: set or not, from where, never what. */
export type SecretStatus = ConfigSecretMeta;

/** The write: plain keys by nested object, secrets by name. */
export interface SecretWrites {
  /** Plaintext to store, by secret key name. An empty string is not a clear: use `clear_secrets`. */
  secrets?: Record<string, string>;
  /** Secret key names to clear. */
  clear_secrets?: string[];
}

/** The body of a PUT: what to set and what to clear, all or nothing. */
export interface ConfigUpdateBody {
  set: Record<string, unknown>;
  unset: string[];
}

/** JSON with object keys sorted, so two spellings of one value compare equal. */
export function canonicalJSON(value: unknown): string {
  if (value === undefined) return 'undefined';
  if (value === null || typeof value !== 'object') return JSON.stringify(value);
  if (Array.isArray(value)) return `[${value.map(canonicalJSON).join(',')}]`;
  const obj = value as Record<string, unknown>;
  const parts = Object.keys(obj)
    .sort()
    .filter((k) => obj[k] !== undefined)
    .map((k) => `${JSON.stringify(k)}:${canonicalJSON(obj[k])}`);
  return `{${parts.join(',')}}`;
}

const isPlainObject = (v: unknown): v is Record<string, unknown> =>
  typeof v === 'object' && v !== null && !Array.isArray(v);

/** Where a dotted key name goes in a nested object. A scalar in the way is left alone. */
function assignPath(target: Record<string, unknown>, name: string, value: unknown): void {
  const segs = name.split('.');
  let node = target;
  for (const seg of segs.slice(0, -1)) {
    const next = node[seg];
    if (next === undefined) {
      const fresh: Record<string, unknown> = {};
      node[seg] = fresh;
      node = fresh;
    } else if (isPlainObject(next)) {
      node = next;
    } else {
      return;
    }
  }
  const last = segs[segs.length - 1];
  const existing = node[last];
  node[last] =
    isPlainObject(existing) && isPlainObject(value) ? { ...existing, ...value } : value;
}

/** The result of reading a View: the nested config the page edits, plus the baseline a save is diffed against. */
export interface ParsedView {
  /** Nested config, secrets left out. */
  config: Record<string, unknown>;
  /** Secret key name to what the secret store says. */
  secretStatus: Record<string, SecretStatus>;
  /** Every writable-or-readable plain key's current value, by flat key name. */
  baseline: Map<string, unknown>;
  /** Every key name the server serves, secrets included. */
  known: Set<string>;
  /** Key names that are secrets. */
  secretNames: Set<string>;
  envLocked: string[];
  settingLocks: Record<string, string>;
}

/** Fold a View's keys into the nested config, status, locks and baseline. */
export function parseView(view: ConfigView): ParsedView {
  const out: ParsedView = {
    config: {},
    secretStatus: {},
    baseline: new Map(),
    known: new Set(),
    secretNames: new Set(),
    envLocked: [],
    settingLocks: {},
  };
  // Shorter names first, so a parent object exists before its dotted children.
  const keys = [...(view.keys ?? [])].sort((a, b) => a.name.localeCompare(b.name));
  for (const k of keys) {
    out.known.add(k.name);
    if (k.locked_by) {
      out.envLocked.push(k.name);
      out.settingLocks[k.name] = k.locked_by;
    }
    if (k.class === 's') {
      out.secretNames.add(k.name);
      out.secretStatus[k.name] = k.secret ?? { set: false };
      continue;
    }
    if (k.value === undefined) continue;
    out.baseline.set(k.name, k.value);
    assignPath(out.config, k.name, k.value);
  }
  return out;
}

/**
 * Turn the nested object a page edits into the keys that CHANGED. A name the
 * server serves is a leaf and goes whole; any other object is walked, its
 * children joined with dots. Keys equal to the baseline are dropped: the server
 * judges every key a PUT names (a locked key is refused even with the value it
 * has), so an unchanged key must not be named. A key the server does not serve
 * is sent as it is, and the server's refusal names it.
 */
export function diffUpdate(
  updates: Record<string, unknown>,
  known: ReadonlySet<string>,
  baseline: ReadonlyMap<string, unknown>
): Record<string, unknown> {
  const set: Record<string, unknown> = {};
  const walk = (prefix: string, value: unknown) => {
    if (value === undefined) return;
    if (prefix !== '' && (known.has(prefix) || !isPlainObject(value))) {
      if (!baseline.has(prefix) || canonicalJSON(baseline.get(prefix)) !== canonicalJSON(value)) {
        set[prefix] = value;
      }
      return;
    }
    if (isPlainObject(value)) {
      for (const [k, v] of Object.entries(value)) walk(prefix === '' ? k : `${prefix}.${k}`, v);
    }
  };
  walk('', updates);
  return set;
}

/** The PUT body for a set of page edits and secret writes. */
export function buildUpdateBody(
  changed: Record<string, unknown>,
  writes: SecretWrites
): ConfigUpdateBody {
  const set: Record<string, unknown> = { ...changed };
  for (const [name, plaintext] of Object.entries(writes.secrets ?? {})) {
    if (plaintext === '') continue; // a blank field means "leave it", never "clear"
    set[name] = plaintext;
  }
  const unset = [...(writes.clear_secrets ?? [])];
  for (const name of unset) {
    delete set[name];
  }
  return { set, unset };
}
