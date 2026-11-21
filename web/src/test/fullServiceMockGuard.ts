// file: web/src/test/fullServiceMockGuard.ts
// version: 0.1.0
// guid: 43654188-6014-473a-9d40-c861dc114028
// last-edited: 2026-11-21

// Finds tests that replace a big service module (`services/api`, `services/versionApi`, ...) with a
// hand-written object and no hold on the real module. Such a mock lists the exports the test thought
// of, so the day any code under test starts using another export, vitest throws "No X export is
// defined on the mock" and the test (or, when a catch swallows it, its UI) breaks. It broke Library
// and the five App tests that way. A partial mock keeps every real export and overrides only what the
// test drives:
//
//   vi.mock('../services/api', async (importOriginal) => ({
//     ...(await importOriginal<typeof import('../services/api')>()),
//     getBooks: vi.fn(),
//   }));
//
// `fullServiceMockGuard.test.ts` runs this over every test file.

/** The modules a hand-written full mock is refused for: src/services/api.ts and the *Api.ts splits. */
const GUARDED = /(^|\/)services\/(api|[A-Za-z]+Api)$/;

/** The ways a factory takes hold of the real module: vi.importActual, or the shared App helper. */
const KEEPS_REAL_MODULE = /\bimportActual\s*[<(]|\bcreateAppApiMock\s*\(/;

/**
 * Whether the factory is `async (name) => ...` and calls `name`, which is what vitest's
 * importOriginal is, whatever the test calls it. A parameter that is declared and never called does
 * not keep anything.
 */
function callsItsImportOriginal(factory: string): boolean {
  const head = /^async\s*\(?\s*([A-Za-z_$][\w$]*)\s*\)?\s*=>/.exec(factory);
  if (!head) return false;
  return new RegExp(`\\b\\${head[1]}\\s*[<(]`).test(factory.slice(head[0].length));
}

export interface FullMock {
  /** The module specifier as written in the test. */
  spec: string;
  /** 1-based line of the vi.mock call. */
  line: number;
}

/** Replaces comments with spaces (newlines kept), leaving strings alone, so line numbers survive. */
export function blankComments(src: string): string {
  let out = '';
  let i = 0;
  let quote: string | null = null;
  while (i < src.length) {
    const c = src[i];
    const next = src[i + 1];
    if (quote) {
      out += c;
      if (c === '\\') {
        out += next ?? '';
        i += 2;
        continue;
      }
      if (c === quote) quote = null;
      i++;
    } else if (c === '"' || c === "'" || c === '`') {
      quote = c;
      out += c;
      i++;
    } else if (c === '/' && next === '/') {
      while (i < src.length && src[i] !== '\n') {
        out += ' ';
        i++;
      }
    } else if (c === '/' && next === '*') {
      const end = src.indexOf('*/', i + 2);
      const stop = end === -1 ? src.length : end + 2;
      out += src.slice(i, stop).replace(/[^\n]/g, ' ');
      i = stop;
    } else {
      out += c;
      i++;
    }
  }
  return out;
}

/** The text between the parens of the call whose '(' sits just before `from`, strings skipped. */
function callArguments(src: string, from: number): string {
  let depth = 1;
  let quote: string | null = null;
  let i = from;
  for (; i < src.length && depth > 0; i++) {
    const c = src[i];
    if (quote) {
      if (c === '\\') i++;
      else if (c === quote) quote = null;
    } else if (c === '"' || c === "'" || c === '`') quote = c;
    else if (c === '(') depth++;
    else if (c === ')') depth--;
  }
  return src.slice(from, i - 1);
}

/**
 * Every vi.mock / vi.doMock in `source` that gives a guarded module a factory which does not keep the
 * real module. `file` is the test's path under src, such as "src/services/api.test.ts": a relative
 * specifier is resolved against it. A call with no factory (an automock) is not flagged.
 */
export function findFullServiceMocks(source: string, file: string): FullMock[] {
  const src = blankComments(source);
  const found: FullMock[] = [];
  for (const m of src.matchAll(/\bvi\.(?:mock|doMock)\(/g)) {
    const args = callArguments(src, m.index! + m[0].length);
    const first = /^\s*(['"`])([^'"`]+)\1\s*(,|$)/.exec(args);
    if (!first) continue;
    const spec = first[2];
    const factory = args.slice(first[0].length).trim();
    if (!factory || KEEPS_REAL_MODULE.test(factory) || callsItsImportOriginal(factory)) continue;
    if (!GUARDED.test(resolveSpec(file, spec))) continue;
    found.push({ spec, line: src.slice(0, m.index).split('\n').length });
  }
  return found;
}

function resolveSpec(file: string, spec: string): string {
  if (!spec.startsWith('.')) return spec.replace(/^@\//, 'src/');
  const parts = file.split('/').slice(0, -1);
  for (const seg of spec.split('/')) {
    if (seg === '..') parts.pop();
    else if (seg !== '.') parts.push(seg);
  }
  return parts.join('/');
}
