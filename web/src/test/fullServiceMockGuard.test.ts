// file: web/src/test/fullServiceMockGuard.test.ts
// version: 0.1.0
// guid: da002804-75bf-4f86-b834-e0514c238289
// last-edited: 2026-11-21

// A hand-written full mock of services/api broke Library (#4910) and 28 App tests (#4920) when an
// export was added. This keeps the class out: no test may give services/api (or a services/*Api
// split) a factory that does not keep the real module. See fullServiceMockGuard.ts for the pattern.

import { describe, expect, it } from 'vitest';
import { blankComments, findFullServiceMocks } from './fullServiceMockGuard';

// Every test file's source, read by Vite, so the scan needs no node fs.
const sources = import.meta.glob('/src/**/*.test.{ts,tsx}', {
  query: '?raw',
  import: 'default',
  eager: true,
}) as Record<string, string>;

describe('no hand-written full mock of a big service module', () => {
  it('scans the test files', () => {
    // a glob that matched nothing would pass the check below for the wrong reason
    expect(Object.keys(sources).length).toBeGreaterThan(100);
  });

  it('finds none', () => {
    const found = Object.entries(sources)
      // this file quotes the bad shape in its own samples
      .filter(([path]) => !path.endsWith('/fullServiceMockGuard.test.ts'))
      .flatMap(([path, source]) =>
        findFullServiceMocks(source, path.slice(1)).map(
          (m) => `${path.slice(1)}:${m.line} mocks ${m.spec}`
        )
      );
    expect(
      found,
      'These replace a big service module with a hand-written object, so any export the code under ' +
        'test starts using throws "No X export is defined on the mock". Spread the real module: ' +
        'vi.mock(spec, async (importOriginal) => ({ ...(await importOriginal<typeof import(spec)>()), ...overrides }))'
    ).toEqual([]);
  });
});

describe('findFullServiceMocks', () => {
  const at = 'src/pages/Library.test.tsx';
  const find = (src: string, file = at) => findFullServiceMocks(src, file);

  it('flags a hand-written object factory, with its line', () => {
    const src = [
      "import x from 'y';",
      "vi.mock('../services/api', () => ({ getBooks: vi.fn() }));",
    ].join('\n');
    expect(find(src)).toEqual([{ spec: '../services/api', line: 2 }]);
  });

  it('flags block factories, async ones, doMock, and the *Api splits', () => {
    expect(find("vi.mock('../services/api', () => { return {}; });")).toHaveLength(1);
    expect(find("vi.mock('../services/api', async () => ({}));")).toHaveLength(1);
    expect(find("vi.doMock('../services/api', () => ({}));")).toHaveLength(1);
    expect(find("vi.mock('../services/versionApi', () => ({}));")).toHaveLength(1);
    expect(find("vi.mock('../../services/activityApi', () => ({}));")).toHaveLength(1);
  });

  it('flags a bare "./api" from inside src/services, and an @/ alias', () => {
    expect(find("vi.mock('./api', () => ({}));", 'src/services/other.test.ts')).toHaveLength(1);
    expect(find("vi.mock('@/services/api', () => ({}));")).toHaveLength(1);
  });

  it('accepts a factory that keeps the real module', () => {
    expect(
      find(
        "vi.mock('../services/api', async (importOriginal) => ({ ...(await importOriginal()) }));"
      )
    ).toEqual([]);
    expect(
      find(
        "vi.mock('../services/api', async () => ({ ...(await vi.importActual('../services/api')) }));"
      )
    ).toEqual([]);
    expect(find("vi.mock('./services/api', async (o) => createAppApiMock(await o()));")).toEqual(
      []
    );
    expect(
      find(
        "vi.mock('../services/api', async (orig) => ({ ...(await orig<typeof import('../services/api')>()) }));"
      )
    ).toEqual([]);
  });

  it('flags a factory that takes importOriginal and never calls it', () => {
    expect(
      find("vi.mock('../services/api', async (importOriginal) => ({ getBooks: vi.fn() }));")
    ).toHaveLength(1);
    expect(
      find("vi.mock('../services/api', (importOriginal) => ({ getBooks: vi.fn() }));")
    ).toHaveLength(1);
  });

  it('accepts an automock, other modules, and a real-module hold that is only a call away', () => {
    expect(find("vi.mock('../services/api');")).toEqual([]);
    expect(find("vi.mock('../components/toast/ToastProvider', () => ({}));")).toEqual([]);
    expect(find("vi.mock('../services/coreEvents', () => ({}));")).toEqual([]);
    expect(find("vi.mock('../services/apiary', () => ({}));")).toEqual([]);
    expect(find("vi.mock('../services/api.helpers', () => ({}));")).toEqual([]);
  });

  it('reads the factory to its closing paren, not to the next importOriginal in the file', () => {
    const src = [
      "vi.mock('../services/api', () => ({ getBooks: vi.fn() }));",
      "vi.mock('../other', async (importOriginal) => ({ ...(await importOriginal()) }));",
    ].join('\n');
    expect(find(src)).toHaveLength(1);
  });

  it('ignores a mock that only appears in a comment, and keeps line numbers', () => {
    const src = [
      "// vi.mock('../services/api', () => ({}));",
      "/* vi.mock('../services/api', () => ({})); */",
      "vi.mock('../services/api', () => ({}));",
    ].join('\n');
    expect(find(src)).toEqual([{ spec: '../services/api', line: 3 }]);
  });

  it('does not read a // inside a string as a comment', () => {
    const src = "const u = 'http://x'; vi.mock('../services/api', () => ({ a: 1 }));";
    expect(find(src)).toHaveLength(1);
  });

  it('blankComments keeps length and newlines', () => {
    const src = 'a // b\n/* c\nd */ e';
    const out = blankComments(src);
    expect(out).toHaveLength(src.length);
    expect(out.split('\n')).toHaveLength(3);
  });
});
