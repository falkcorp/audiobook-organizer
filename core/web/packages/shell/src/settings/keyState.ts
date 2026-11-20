// file: core/web/packages/shell/src/settings/keyState.ts
// version: 0.1.0
// guid: 84558d3b-7515-4a7f-bc64-4cabaafa109f
// last-edited: 2026-11-20

// Pure helpers of the schema-driven settings renderer (07 §7.4.5): which widget a
// key gets, the client-side format and range checks, ordering and comparison.

import type { JsonSchema, KeyStateView } from '../api/interim';

export type WidgetKind = 'secret' | 'select' | 'switch' | 'number' | 'text' | 'list' | 'readonly';

/** The JSON Schema type, ignoring a "null" alternative. */
export function primaryType(s: JsonSchema | undefined): string | undefined {
  if (!s) return undefined;
  if (Array.isArray(s.type)) return s.type.find((t) => t !== 'null');
  return s.type;
}

export function isSecretKey(k: KeyStateView): boolean {
  return k.secret !== undefined || k.schema.writeOnly === true || k.schema['x-mediacore-secret'] === true;
}

/** An array of strings or numbers is a chip list; anything richer is a module panel (§7.4.5). */
export function listItemType(s: JsonSchema): 'string' | 'number' | 'integer' | null {
  const t = primaryType(s.items);
  return t === 'string' || t === 'number' || t === 'integer' ? t : null;
}

export function widgetFor(k: KeyStateView): WidgetKind {
  if (isSecretKey(k)) return 'secret';
  const s = k.schema;
  const t = primaryType(s);
  if (t === 'boolean') return 'switch';
  if (Array.isArray(s.enum) && s.enum.length > 0) return 'select';
  if (t === 'integer' || t === 'number') return 'number';
  if (t === 'string') return 'text';
  if (t === 'array' && listItemType(s)) return 'list';
  return 'readonly';
}

export function keyLabel(k: KeyStateView): string {
  return k.schema.title || k.path.slice(k.path.lastIndexOf('.') + 1);
}

export function sectionKeys(keys: readonly KeyStateView[], section: string): KeyStateView[] {
  return keys
    .filter((k) => k.section === section)
    .sort((a, b) => {
      const oa = a.schema['x-mediacore-ui']?.order ?? Number.MAX_SAFE_INTEGER;
      const ob = b.schema['x-mediacore-ui']?.order ?? Number.MAX_SAFE_INTEGER;
      return oa - ob || a.path.localeCompare(b.path);
    });
}

export function jsonEqual(a: unknown, b: unknown): boolean {
  return JSON.stringify(a) === JSON.stringify(b);
}

const FORMATS: Record<string, (v: string) => boolean> = {
  // Go's time.ParseDuration: a sequence of <number><unit>, or "0".
  duration: (v) => v === '0' || /^[+-]?(\d+(\.\d+)?(ns|us|µs|μs|ms|s|m|h))+$/.test(v),
  uri: (v) => /^[a-z][a-z0-9+.-]*:\/?\/?\S+$/i.test(v),
  cidr: (v) => {
    const m = /^([0-9a-f:.]+)\/(\d{1,3})$/i.exec(v);
    if (!m) return false;
    const bits = Number(m[2]);
    return m[1].includes(':') ? bits <= 128 : /^\d{1,3}(\.\d{1,3}){3}$/.test(m[1]) && bits <= 32;
  },
  path: (v) => !v.includes('\0'),
  email: (v) => /^[^\s@]+@[^\s@]+\.[^\s@]+$/.test(v),
};

/** A message for a string that fails its `format`, else null. The server's validation is authoritative. */
export function formatError(format: string | undefined, value: string): string | null {
  if (!format || value === '') return null;
  const ok = FORMATS[format];
  return ok && !ok(value) ? `Not a valid ${format}.` : null;
}

/** Parses the text of a number field; an error message when it is not acceptable. */
export function parseNumber(s: JsonSchema, text: string): { value: number } | { error: string } {
  const n = Number(text);
  if (text.trim() === '' || !Number.isFinite(n)) return { error: 'Enter a number.' };
  if (primaryType(s) === 'integer' && !Number.isInteger(n)) return { error: 'Enter a whole number.' };
  if (s.minimum !== undefined && n < s.minimum) return { error: `Must be at least ${s.minimum}.` };
  if (s.maximum !== undefined && n > s.maximum) return { error: `Must be at most ${s.maximum}.` };
  return { value: n };
}

/** Parses the chips of a list field into the array to store; an error message when one is not acceptable. */
export function parseList(s: JsonSchema, chips: readonly string[]): { value: (string | number)[] } | { error: string } {
  const itemType = listItemType(s);
  if (itemType === 'string') {
    const bad = chips.map((c) => formatError(s.items?.format, c)).find((e) => e !== null);
    return bad ? { error: bad } : { value: [...chips] };
  }
  const out: (string | number)[] = [];
  for (const c of chips) {
    const n = parseNumber(s.items ?? {}, c);
    if ('error' in n) return { error: `"${c}": ${n.error}` };
    out.push(n.value);
  }
  return { value: out };
}
