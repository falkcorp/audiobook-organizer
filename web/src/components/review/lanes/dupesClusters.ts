// file: web/src/components/review/lanes/dupesClusters.ts
// version: 1.1.0
// guid: 3c8e5a91-7d24-4f60-b1a9-2e6d0f4c8b73
// last-edited: 2026-10-12
//
// Connected components of the loaded page's candidate pairs: the "Clusters"
// view of the Duplicates lane.
//
// Lifted from DedupEmbeddingTab's buildClusters (that tab is being retired), as
// a pure function so it can be tested without a DOM.
//
// PAGE BOUNDARY (spec 03, section 5). The server pages PAIRS, not clusters, so a
// cluster built here is the connected component of the pairs on THIS page only.
// A book that also appears in pairs on another page yields a smaller cluster
// here than it would over the whole queue, and the same book can sit in a
// cluster on page 1 and in a different one on page 2. That is a property of the
// input, not a bug in the union-find; the card says so (see useClusterOverflow)
// rather than pretending the component is complete. There is deliberately no
// server clustering endpoint (owner decision D22: per-page now, server-side later).

import type { Book, DedupCandidate } from '../../../services/api';
import { metadataQuality } from './keepDecision';

export interface DupeClusterMember {
  bookId: string;
  /** The inline book from include_books, or null when the lookup missed. */
  book: Book | null;
}

export interface DupeCluster {
  /** The smallest member book id: stable across refetches, unlike a union-find root. */
  key: string;
  /** Member book ids in first-seen order. These are BOOK ids, not candidate ids. */
  bookIds: string[];
  members: DupeClusterMember[];
  /** The pairs (candidate ids) that connect the members, ascending. */
  candidateIds: number[];
  /** The strongest layer seen across the pairs (exact > llm > embedding). */
  layer: string;
  maxSimilarity: number | null;
  hasPending: boolean;
  /** The one status every pair shares, or 'mixed'. */
  overallStatus: string;
  llmInfo: string | null;
}

const LAYER_RANK: Record<string, number> = { exact: 3, llm: 2, embedding: 1 };

/**
 * Group the page's pairs into clusters with union-find. Clusters are ordered by
 * the lowest candidate id they contain, so the order is stable across refreshes.
 */
export function buildClusters(candidates: readonly DedupCandidate[]): DupeCluster[] {
  const parent = new Map<string, string>();
  const find = (x: string): string => {
    let root = x;
    while (parent.get(root) !== root) root = parent.get(root)!;
    let cur = x;
    while (parent.get(cur) !== root) {
      const next = parent.get(cur)!;
      parent.set(cur, root);
      cur = next;
    }
    return root;
  };
  for (const c of candidates) {
    if (!parent.has(c.entity_a_id)) parent.set(c.entity_a_id, c.entity_a_id);
    if (!parent.has(c.entity_b_id)) parent.set(c.entity_b_id, c.entity_b_id);
    const ra = find(c.entity_a_id);
    const rb = find(c.entity_b_id);
    if (ra !== rb) parent.set(ra, rb);
  }

  const groups = new Map<string, DupeCluster>();
  const seen = new Map<string, Set<string>>();
  for (const c of candidates) {
    const root = find(c.entity_a_id);
    let g = groups.get(root);
    if (!g) {
      g = {
        key: root,
        bookIds: [],
        members: [],
        candidateIds: [],
        layer: c.layer,
        maxSimilarity: c.similarity ?? null,
        hasPending: false,
        overallStatus: c.status,
        llmInfo: null,
      };
      groups.set(root, g);
      seen.set(root, new Set());
    }
    const have = seen.get(root)!;
    const sides: Array<[string, Book | null | undefined]> = [
      [c.entity_a_id, c.book_a],
      [c.entity_b_id, c.book_b],
    ];
    for (const [id, book] of sides) {
      if (!have.has(id)) {
        have.add(id);
        g.bookIds.push(id);
        g.members.push({ bookId: id, book: book ?? null });
      } else if (book) {
        // The first pair that carried this book wins, unless it carried none.
        const m = g.members.find((x) => x.bookId === id);
        if (m && !m.book) m.book = book;
      }
    }
    g.candidateIds.push(c.id);
    if ((LAYER_RANK[c.layer] ?? 0) > (LAYER_RANK[g.layer] ?? 0)) g.layer = c.layer;
    if (c.similarity != null && (g.maxSimilarity == null || c.similarity > g.maxSimilarity)) {
      g.maxSimilarity = c.similarity;
    }
    if (c.status === 'pending') g.hasPending = true;
    if (g.overallStatus !== c.status) g.overallStatus = 'mixed';
    if (c.llm_reason && !g.llmInfo) g.llmInfo = `${c.llm_verdict ?? ''}: ${c.llm_reason}`;
  }

  const out = Array.from(groups.values());
  for (const g of out) {
    g.candidateIds.sort((a, b) => a - b);
    g.key = g.bookIds.reduce((min, id) => (id < min ? id : min));
  }
  return out.sort((a, b) => a.candidateIds[0] - b.candidateIds[0]);
}

/**
 * The member the card pre-selects as primary: the one with strictly the richest
 * metadata, or null when two or more tie for it.
 *
 * Null is an answer: the link-cluster endpoint then auto-picks (path origin,
 * curation, format, bitrate, size), which is better evidence than a coin flip
 * presented as a recommendation. This is a per-page client guess, standing in
 * for the server-side "recommended" that owner decision D22 defers.
 */
export function recommendedPrimary(cluster: DupeCluster): string | null {
  let best: string | null = null;
  let bestScore = -Infinity;
  let tied = false;
  for (const m of cluster.members) {
    const score = metadataQuality(m.book);
    if (score > bestScore) {
      best = m.bookId;
      bestScore = score;
      tied = false;
    } else if (score === bestScore) {
      tied = true;
    }
  }
  return tied ? null : best;
}

/** The pair ids the loaded page holds for these books: both ends inside the set. */
export function pairIdsWithin(
  candidates: readonly DedupCandidate[],
  bookIds: readonly string[]
): number[] {
  const set = new Set(bookIds);
  return candidates
    .filter((c) => set.has(c.entity_a_id) && set.has(c.entity_b_id))
    .map((c) => c.id);
}

/** "N more pairs for these books are on other pages", singular-safe. */
export function otherPagesLabel(count: number): string {
  return count === 1
    ? '1 more pair for these books is on another page'
    : `${count.toLocaleString()} more pairs for these books are on other pages`;
}
