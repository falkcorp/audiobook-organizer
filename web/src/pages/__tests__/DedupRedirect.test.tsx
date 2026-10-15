// file: web/src/pages/__tests__/DedupRedirect.test.tsx
// version: 1.0.1
// guid: 91b7d3a5-2c48-4e60-b5f9-0d1e7a8c3624
// last-edited: 2026-10-15

import { describe, it, expect } from 'vitest';
import { render, screen } from '@testing-library/react';
import { MemoryRouter, Route, Routes, useLocation, useNavigationType } from 'react-router-dom';
import { DedupRedirect } from '../DedupRedirect';

function Where() {
  const { pathname, search } = useLocation();
  return <div data-testid="where">{pathname + search}</div>;
}

function renderAt(url: string) {
  return render(
    <MemoryRouter initialEntries={[url]}>
      <Routes>
        {['/dedup', '/dedup/labels', '/authors/dedup', '/books/dedup'].map((p) => (
          <Route key={p} path={p} element={<DedupRedirect />} />
        ))}
        <Route path="/review" element={<Where />} />
      </Routes>
    </MemoryRouter>,
  );
}

async function landed(url: string): Promise<string> {
  renderAt(url);
  return (await screen.findByTestId('where')).textContent ?? '';
}

describe('DedupRedirect', () => {
  const rows: Array<[string, string, string]> = [
    ['books', '/dedup?tab=books', '/review?lane=dupes&layer=exact'],
    ['book-duplicates', '/dedup?tab=book-duplicates', '/review?lane=dupes&layer=exact'],
    ['embedding', '/dedup?tab=embedding', '/review?lane=dupes'],
    ['acoustic', '/dedup?tab=acoustic', '/review?lane=dupes&layer=acoustid'],
    ['authors', '/dedup?tab=authors', '/review?lane=authors'],
    ['series', '/dedup?tab=series', '/review?lane=authors&view=series'],
    ['ai', '/dedup?tab=ai', '/review?lane=authors&view=ai'],
    ['reconcile', '/dedup?tab=reconcile', '/review?lane=repairs&fixer=reconcile.missing-files'],
    ['split-books', '/dedup?tab=split-books', '/review?lane=repairs&fixer=dedup.split-books'],
    ['labels path', '/dedup/labels', '/review?lane=dupes&view=labels'],
  ];
  it.each(rows)('maps %s', async (_n, from, to) => {
    expect(await landed(from)).toBe(to);
  });

  it('with no tab lands on the exact layer, from all four legacy paths', async () => {
    for (const p of ['/dedup', '/authors/dedup', '/books/dedup']) {
      document.body.innerHTML = '';
      expect(await landed(p)).toBe('/review?lane=dupes&layer=exact');
    }
  });

  it('an unknown tab keeps book and band and drops every other param', async () => {
    expect(await landed('/dedup?tab=unified&book=b-1&band=likely&foo=1')).toBe(
      '/review?lane=dupes&book=b-1&band=likely',
    );
  });

  it('keeps book and band on a mapped tab and on the labels path', async () => {
    expect(await landed('/dedup?tab=acoustic&book=b-2&band=sure&x=y')).toBe(
      '/review?lane=dupes&layer=acoustid&book=b-2&band=sure',
    );
    document.body.innerHTML = '';
    expect(await landed('/dedup/labels?book=b-3&junk=1')).toBe(
      '/review?lane=dupes&view=labels&book=b-3',
    );
  });

  it('replaces the history entry so Back skips the redirecting URL', async () => {
    function Type() {
      return <div data-testid="type">{useNavigationType()}</div>;
    }
    render(
      <MemoryRouter initialEntries={['/dedup?tab=authors']}>
        <Routes>
          <Route path="/dedup" element={<DedupRedirect />} />
          <Route path="/review" element={<Type />} />
        </Routes>
      </MemoryRouter>,
    );
    // PUSH would leave /dedup behind /review, and Back would bounce forward again.
    expect((await screen.findByTestId('type')).textContent).toBe('REPLACE');
  });
});
