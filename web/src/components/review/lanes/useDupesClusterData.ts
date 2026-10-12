// file: web/src/components/review/lanes/useDupesClusterData.ts
// version: 1.5.0
// guid: d47a1c85-2e60-4b93-8f1d-6a9c3e5b0a72
// last-edited: 2026-10-12
//
// The two fetches only the Clusters view needs, split out of useDupesLane so
// that file does not grow a third concern. Both are called from useDupesLane
// and surfaced on its return value: the lane is still the one hook.
//
//   useClusterOverflow  how many more pairs a cluster's books have on other pages
//   useSeriesSummary    the By-series list and its link-all action

import { useCallback, useEffect, useMemo, useRef, useState } from 'react';
import * as api from '../../../services/api';
import type { DedupCandidate, DedupSeriesSummary } from '../../../services/api';
import { isAbortError } from '../../../utils/apiFetch';
import type { Toast } from './useMetadataLane';
import type { DupeCluster } from './dupesClusters';

export type ClusterOverflow =
  | { state: 'loading' }
  | { state: 'error'; message: string }
  | {
      state: 'ready';
      /** Distinct pairs for the cluster's books that are not on the loaded page. */
      count: number;
      /** The books that have such pairs, with their own counts: what the count opens. */
      perBook: Array<{ bookId: string; count: number }>;
    };

type ClusterAnswer =
  | { error: string }
  | { count: number; perBook: Array<{ bookId: string; count: number }> };

/**
 * For each cluster on the loaded page, the pairs its books have on OTHER pages.
 *
 * The server pages pairs, so a book can have pairs the page did not reach (see
 * dupesClusters.ts). ONE request (book-pair-counts) sends the page's clusters,
 * the page's own pair ids and the list's filter (`filterParams`, no paging, no
 * entity_id); the server answers for every cluster from a single candidate
 * scan. This used to be one entity_id request per book, about 200 per page,
 * repeated on every refresh.
 *
 * Only runs while `active`: a lane in another view mode makes no extra requests.
 */
export function useClusterOverflow(
  active: boolean,
  clusters: readonly DupeCluster[],
  pageCandidates: readonly DedupCandidate[],
  filterParams: Parameters<typeof api.getDedupCandidates>[0],
  reloadToken: number
): (cluster: DupeCluster) => ClusterOverflow {
  const pageIds = useMemo(() => pageCandidates.map((c) => c.id), [pageCandidates]);
  // Answers are stored WITH the request key they answer. A change of page,
  // filter or refresh changes the key, which makes every stored answer stale
  // for the render that sees the new key -- derived, so no effect has to clear
  // state and no card ever reads another page's answer as its own.
  const requestKey = JSON.stringify([
    filterParams ?? {},
    clusters.map((c) => [c.key, c.bookIds]),
    pageIds,
    reloadToken,
  ]);
  const [answers, setAnswers] = useState<{
    key: string;
    byCluster: ReadonlyMap<string, ClusterAnswer>;
  }>(() => ({ key: '', byCluster: new Map() }));
  const byCluster = answers.key === requestKey ? answers.byCluster : NO_ANSWERS;
  // The latest inputs, read inside the effect so a new object identity with the
  // same content (requestKey) does not refetch.
  const inputs = useRef({ clusters, pageIds, filterParams });
  useEffect(() => {
    inputs.current = { clusters, pageIds, filterParams };
  });

  useEffect(() => {
    const { clusters: cs, pageIds: ids, filterParams: fp } = inputs.current;
    if (!active || cs.length === 0) return;
    const ctrl = new AbortController();
    api
      .getDedupBookPairCounts(
        {
          clusters: cs.map((c) => ({ key: c.key, book_ids: c.bookIds })),
          page_candidate_ids: ids,
          status: fp?.status,
          layer: fp?.layer,
          band: fp?.band,
          q: fp?.q,
          both_unmatched: fp?.both_unmatched,
          entity_id: fp?.entity_id,
        },
        { signal: ctrl.signal }
      )
      .then((rows) => {
        if (ctrl.signal.aborted) return;
        const map = new Map<string, ClusterAnswer>();
        for (const r of rows) {
          map.set(r.key, {
            count: r.other_pairs,
            perBook: r.per_book.map((b) => ({ bookId: b.book_id, count: b.count })),
          });
        }
        setAnswers({ key: requestKey, byCluster: map });
      })
      .catch((err: unknown) => {
        if (ctrl.signal.aborted || isAbortError(err)) return;
        const message = err instanceof Error ? err.message : 'Could not count the other pairs';
        setAnswers({
          key: requestKey,
          byCluster: new Map(cs.map((c) => [c.key, { error: message }] as const)),
        });
      });
    return () => ctrl.abort();
  }, [active, requestKey]);

  return useCallback(
    (cluster: DupeCluster): ClusterOverflow => {
      const a = byCluster.get(cluster.key);
      if (!a) return { state: 'loading' };
      if ('error' in a) return { state: 'error', message: a.error };
      return { state: 'ready', count: a.count, perBook: a.perBook };
    },
    [byCluster]
  );
}

const NO_ANSWERS: ReadonlyMap<string, ClusterAnswer> = new Map();

export type SeriesSummaryState =
  | { state: 'idle' }
  | { state: 'loading' }
  | { state: 'error'; message: string }
  | { state: 'ready'; rows: DedupSeriesSummary[] };

export interface DupesSeries {
  summary: SeriesSummaryState;
  /** The series whose link-all is running, or null. One at a time. */
  linking: number | null;
  /** Link every pending pair in a series. The caller has already confirmed. */
  linkSeries: (seriesId: number) => Promise<void>;
}

/**
 * The By-series list: series that have pending cluster candidates, with counts.
 * Fetched when `active` turns on and re-fetched after every refresh, so the
 * counts match the server's current state.
 */
export function useSeriesSummary(
  active: boolean,
  reloadToken: number,
  toast: Toast,
  refresh: () => void
): DupesSeries {
  // The answer is stored with the refresh it answers; a newer refresh makes it
  // stale, which reads as "loading" until the new answer lands (derived, so no
  // effect has to reset state).
  const [answer, setAnswer] = useState<{
    token: number;
    value: Exclude<SeriesSummaryState, { state: 'idle' | 'loading' }>;
  } | null>(null);
  const [linking, setLinking] = useState<number | null>(null);

  useEffect(() => {
    if (!active) return;
    const ctrl = new AbortController();
    api
      .listDedupCandidateSeries({ signal: ctrl.signal })
      .then((rows) => {
        if (!ctrl.signal.aborted) setAnswer({ token: reloadToken, value: { state: 'ready', rows } });
      })
      .catch((err: unknown) => {
        if (ctrl.signal.aborted || isAbortError(err)) return;
        setAnswer({
          token: reloadToken,
          value: {
            state: 'error',
            message: err instanceof Error ? err.message : 'Failed to load the series summary',
          },
        });
      });
    return () => {
      ctrl.abort();
      // Drop the answer when the switch goes off (or a refresh supersedes it):
      // turning it back on must read "loading", not the previous answer.
      setAnswer(null);
    };
  }, [active, reloadToken]);

  const summary: SeriesSummaryState = !active
    ? { state: 'idle' }
    : answer && answer.token === reloadToken
      ? answer.value
      : { state: 'loading' };

  const linkSeries = useCallback(
    async (seriesId: number) => {
      setLinking(seriesId);
      try {
        const r = await api.linkDedupCandidateSeries(seriesId);
        const failed = r.failures?.length ?? 0;
        toast(
          `Series link: ${r.clusters_merged} cluster(s), ${r.books_merged} book(s) linked` +
            (failed > 0 ? `, ${failed} failed` : ''),
          failed === 0 ? 'success' : 'warning'
        );
      } catch (err) {
        toast(err instanceof Error ? err.message : 'Series link failed', 'error');
      } finally {
        setLinking(null);
        // After the toast, success or not: the list and the summary both moved
        // (or the server refused and they are what they were) and either way
        // the screen must show the server's answer, not a guess.
        refresh();
      }
    },
    [toast, refresh]
  );

  return { summary, linking, linkSeries };
}

export type SeriesPairsState =
  | { state: 'loading' }
  | { state: 'error'; message: string }
  | { state: 'ready'; pairs: DedupCandidate[]; total: number; loadMore: (() => void) | null };

/** Pairs per request in the series pairs list. */
export const SERIES_PAIRS_PAGE = 100;
/** Ceiling for the clusters / books views, which need every pair of the series. */
export const SERIES_ALL_PAIRS_CAP = 5000;

/**
 * The pending pairs behind one series row: what its counts open.
 *
 * One request to the candidate list with `series_id` (both books in the
 * series, resolved by the server at scan level), so `total` is the whole
 * series. By default the list pages normally: the first page loads on open and
 * `loadMore` asks for the next. With `all` set (the clusters and books views,
 * which are built from the pairs and so need all of them) it asks once for up
 * to SERIES_ALL_PAIRS_CAP. Runs only while `seriesId` is non-null (the dialog
 * is open) and aborts on close.
 */
export function useSeriesPairs(seriesId: number | null, all = false): SeriesPairsState {
  const [pages, setPages] = useState({ id: seriesId, n: 1 });
  const n = pages.id === seriesId ? pages.n : 1;
  const limit = all ? SERIES_ALL_PAIRS_CAP : n * SERIES_PAIRS_PAGE;
  const [answer, setAnswer] = useState<{
    id: number;
    all: boolean;
    value: Exclude<SeriesPairsState, { state: 'loading' }>;
  } | null>(null);

  useEffect(() => {
    if (seriesId === null) return;
    const ctrl = new AbortController();
    api
      .getDedupCandidates(
        {
          entity_type: 'book',
          status: 'pending',
          series_id: seriesId,
          include_books: true,
          limit,
          offset: 0,
        },
        { signal: ctrl.signal }
      )
      .then((res) => {
        if (ctrl.signal.aborted) return;
        const pairs = res.candidates ?? [];
        const total = res.total ?? pairs.length;
        setAnswer({
          id: seriesId,
          all,
          value: {
            state: 'ready',
            pairs,
            total,
            loadMore:
              !all && pairs.length < total ? () => setPages({ id: seriesId, n: n + 1 }) : null,
          },
        });
      })
      .catch((err: unknown) => {
        if (ctrl.signal.aborted || isAbortError(err)) return;
        setAnswer({
          id: seriesId,
          all,
          value: {
            state: 'error',
            message: err instanceof Error ? err.message : 'Could not load the series pairs',
          },
        });
      });
    return () => ctrl.abort();
  }, [seriesId, all, limit, n]);

  // A different series or view makes the stored answer stale by identity;
  // growing the pairs list keeps the shown rows until the larger answer lands,
  // so "load more" does not flash the list away.
  return answer && answer.id === seriesId && answer.all === all
    ? answer.value
    : { state: 'loading' };
}
