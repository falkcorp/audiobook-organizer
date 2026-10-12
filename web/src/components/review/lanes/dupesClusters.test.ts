// file: web/src/components/review/lanes/dupesClusters.test.ts
// version: 1.0.0
// guid: 8b2f6d14-5a07-4c93-9e81-0d3a7c5e1f46
// last-edited: 2026-10-12

import { describe, expect, it } from 'vitest';
import type { Book, DedupCandidate } from '../../../services/api';
import { buildClusters, pairIdsWithin, recommendedPrimary } from './dupesClusters';

function book(id: string, extra: Partial<Book> = {}): Book {
  return { id, title: `Book ${id}`, ...extra } as Book;
}

function pair(
  id: number,
  a: string,
  b: string,
  extra: Partial<DedupCandidate> = {}
): DedupCandidate {
  return {
    id,
    entity_type: 'book',
    entity_a_id: a,
    entity_b_id: b,
    layer: 'embedding',
    status: 'pending',
    created_at: '2026-01-01T00:00:00Z',
    updated_at: '2026-01-01T00:00:00Z',
    book_a: book(a),
    book_b: book(b),
    ...extra,
  } as DedupCandidate;
}

describe('buildClusters', () => {
  it('clusters are connected components', () => {
    const clusters = buildClusters([
      pair(1, 'b1', 'b2'),
      pair(2, 'b2', 'b3'),
      pair(3, 'b4', 'b5'),
      pair(4, 'b6', 'b3'),
    ]);
    expect(clusters).toHaveLength(2);
    expect(clusters[0].bookIds.sort()).toEqual(['b1', 'b2', 'b3', 'b6']);
    expect(clusters[0].candidateIds).toEqual([1, 2, 4]);
    expect(clusters[1].bookIds.sort()).toEqual(['b4', 'b5']);
    expect(clusters[1].candidateIds).toEqual([3]);
  });

  it('orders clusters by their lowest pair id and keys them by their smallest book id', () => {
    const clusters = buildClusters([pair(9, 'z2', 'z1'), pair(2, 'y2', 'y1')]);
    expect(clusters.map((c) => c.key)).toEqual(['y1', 'z1']);
  });

  it('keeps the strongest layer, the top similarity and a mixed status', () => {
    const [c] = buildClusters([
      pair(1, 'b1', 'b2', { layer: 'embedding', similarity: 0.8 }),
      pair(2, 'b2', 'b3', { layer: 'exact', similarity: 0.95, status: 'merged' }),
      pair(3, 'b1', 'b3', { layer: 'llm', llm_verdict: 'dup', llm_reason: 'same text' }),
    ]);
    expect(c.layer).toBe('exact');
    expect(c.maxSimilarity).toBe(0.95);
    expect(c.overallStatus).toBe('mixed');
    expect(c.hasPending).toBe(true);
    expect(c.llmInfo).toBe('dup: same text');
  });

  it('carries each member book once, taking the book from whichever pair has it', () => {
    const [c] = buildClusters([
      pair(1, 'b1', 'b2', { book_a: null }),
      pair(2, 'b1', 'b3'),
    ]);
    expect(c.members.map((m) => m.bookId)).toEqual(['b1', 'b2', 'b3']);
    expect(c.members[0].book?.title).toBe('Book b1');
  });

  it('a cluster split across a page boundary comes out as two smaller clusters', () => {
    // b1-b2-b3 is one component over the whole queue; pairs 1 and 2 are on page
    // one and pair 3 is on page two, so each page sees only its own piece.
    const pageOne = buildClusters([pair(1, 'b1', 'b2'), pair(2, 'b2', 'b3')]);
    const pageTwo = buildClusters([pair(3, 'b3', 'b4')]);
    expect(pageOne).toHaveLength(1);
    expect(pageOne[0].bookIds).toEqual(['b1', 'b2', 'b3']);
    expect(pageTwo).toHaveLength(1);
    expect(pageTwo[0].bookIds).toEqual(['b3', 'b4']);
    // b3 sits in both; neither page can see that it is one cluster.
    expect(pageOne[0].bookIds).toContain('b3');
    expect(pageTwo[0].bookIds).toContain('b3');
  });

  it('returns nothing for an empty page', () => {
    expect(buildClusters([])).toEqual([]);
  });
});

describe('recommendedPrimary', () => {
  it('picks the member with strictly the richest metadata', () => {
    const [c] = buildClusters([
      pair(1, 'b1', 'b2', {
        book_a: book('b1'),
        book_b: book('b2', { asin: 'B000000001', isbn13: '9780000000002', cover_url: 'x' }),
      }),
    ]);
    expect(recommendedPrimary(c)).toBe('b2');
  });

  it('is null on a tie, so the server auto-picks', () => {
    const [c] = buildClusters([pair(1, 'b1', 'b2')]);
    expect(recommendedPrimary(c)).toBeNull();
  });

  it('is null when two members tie for the top even if a third is poorer', () => {
    const rich = { asin: 'B000000001', cover_url: 'x' };
    const [c] = buildClusters([
      pair(1, 'b1', 'b2', { book_a: book('b1', rich), book_b: book('b2', rich) }),
      pair(2, 'b2', 'b3', { book_a: book('b2', rich), book_b: book('b3', { title: 'TITLE' }) }),
    ]);
    expect(recommendedPrimary(c)).toBeNull();
  });
});

describe('pairIdsWithin', () => {
  it('returns only the pairs with both ends inside the set', () => {
    const cands = [pair(1, 'b1', 'b2'), pair(2, 'b2', 'b9'), pair(3, 'b2', 'b3')];
    expect(pairIdsWithin(cands, ['b1', 'b2', 'b3'])).toEqual([1, 3]);
  });
});
