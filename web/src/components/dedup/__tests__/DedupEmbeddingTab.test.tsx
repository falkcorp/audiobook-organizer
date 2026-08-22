// file: web/src/components/dedup/__tests__/DedupEmbeddingTab.test.tsx
// version: 1.0.1
// guid: 168ea495-1c5f-44bd-81b6-b01c7bc5d281
// last-edited: 2026-08-21

import { render, screen } from '@testing-library/react';
import { MemoryRouter } from 'react-router-dom';
import { beforeEach, describe, expect, it, vi } from 'vitest';
import { EmbeddingDedupTab } from '../DedupEmbeddingTab';
import * as api from '../../../services/api';
import type { Book, DedupCandidate } from '../../../services/api';

// Only the four calls EmbeddingDedupTab makes on mount are stubbed; everything
// else keeps its real implementation so a member this component's subtree needs
// (but this test does not name) cannot come back undefined and blow up mid-render.
vi.mock('../../../services/api', async (importOriginal) => {
  const actual = await importOriginal<typeof import('../../../services/api')>();
  return {
    ...actual,
    getDedupStats: vi.fn(),
    getDedupCandidates: vi.fn(),
    getBook: vi.fn(),
    getBookFiles: vi.fn(),
  };
});

/**
 * DedupEmbeddingTab.tsx keeps a module-level `bookCache` keyed by book id that
 * `vi.clearAllMocks()` cannot reach, so every test below uses its own pair of
 * book ids. Reusing an id would serve a previous test's coverage value.
 */
function makeBook(id: string, coveragePct: number | null | undefined): Book {
  return {
    id,
    title: `Coverage Fixture ${id}`,
    author_name: `Author ${id}`,
    file_path: `/library/${id}.m4b`,
    created_at: '2026-08-21T00:00:00Z',
    updated_at: '2026-08-21T00:00:00Z',
    book_sig_coverage_pct: coveragePct,
  };
}

function makeCandidate(aID: string, bID: string): DedupCandidate {
  return {
    id: 1,
    entity_type: 'book',
    entity_a_id: aID,
    entity_b_id: bID,
    layer: 'embedding',
    similarity: 0.97,
    status: 'pending',
    created_at: '2026-08-21T00:00:00Z',
    updated_at: '2026-08-21T00:00:00Z',
  };
}

/** Wire the mount-time API surface to a single candidate pair of books. */
function stubPair(bookA: Book, bookB: Book) {
  vi.mocked(api.getDedupStats).mockResolvedValue({ stats: [] });
  vi.mocked(api.getDedupCandidates).mockResolvedValue({
    candidates: [makeCandidate(bookA.id, bookB.id)],
    total: 1,
  });
  vi.mocked(api.getBook).mockImplementation(async (id: string) => {
    if (id === bookA.id) return bookA;
    if (id === bookB.id) return bookB;
    throw new Error(`unexpected getBook(${id})`);
  });
  vi.mocked(api.getBookFiles).mockResolvedValue({ files: [], count: 0 });
}

function renderTab() {
  return render(
    <MemoryRouter>
      <EmbeddingDedupTab />
    </MemoryRouter>
  );
}

describe('EmbeddingDedupTab book-sig coverage badge', () => {
  beforeEach(() => {
    vi.clearAllMocks();
  });

  it('TestBookSigCoverage_RendersPartialBadge: shows "partial fp 62%" for a partially covered book', async () => {
    const bookA = makeBook('cov-partial-a', 62);
    const bookB = makeBook('cov-partial-b', 100);
    stubPair(bookA, bookB);

    renderTab();

    // Positive anchor first: proves the book side actually rendered instead of
    // the `Book #{id}` placeholder that appears when book details never load.
    // Without it, the badge assertions below could pass on an empty card.
    expect(await screen.findByText('Author cov-partial-a')).toBeInTheDocument();
    expect(await screen.findByText('partial fp 62%')).toBeInTheDocument();
  });

  it('TestBookSigCoverage_HidesBadgeAtFullCoverage: no badge at 100% and none when the value is absent', async () => {
    // 100 is the boundary the `< 100` check excludes.
    const fullA = makeBook('cov-full-a', 100);
    const fullB = makeBook('cov-full-b', 100);
    stubPair(fullA, fullB);

    const { unmount } = renderTab();

    expect(await screen.findByText('Author cov-full-a')).toBeInTheDocument();
    expect(await screen.findByText('Author cov-full-b')).toBeInTheDocument();
    // queryAllByText, not queryByText: the single-element query *throws* on two
    // matches, which reports a regression as a lookup error rather than as
    // "the badge rendered when it should not have".
    expect(screen.queryAllByText(/partial fp/)).toHaveLength(0);

    unmount();
    vi.clearAllMocks();

    // Unknown coverage must not throw and must not show the badge either.
    const nullA = makeBook('cov-null-a', null);
    const nullB = makeBook('cov-null-b', undefined);
    stubPair(nullA, nullB);

    renderTab();

    expect(await screen.findByText('Author cov-null-a')).toBeInTheDocument();
    expect(await screen.findByText('Author cov-null-b')).toBeInTheDocument();
    expect(screen.queryAllByText(/partial fp/)).toHaveLength(0);
  });
});
