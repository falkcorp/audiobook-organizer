// file: web/src/components/review/spine/candidateLoader.ts
// version: 1.0.0
// guid: 88122df6-96ce-4289-bd2b-f18276f10984
// last-edited: 2026-10-07
//
// The Candidates view's request discipline, kept out of the components so it
// can be tested without rendering.
//
// - CandidateLoader: ONE queue per spine for the per-book candidate searches
//   (POST /audiobooks/:id/search-metadata, the Search Metadata dialog's
//   endpoint). At most `maxConcurrent` (4) requests in flight. Cards request
//   when they scroll into view and cancel when they leave before their turn,
//   so a fast scroll does not leave a queue of hundreds behind it: a cache
//   miss on that endpoint asks real providers, and Google Books has a daily
//   quota. Answers are memoized per book + query, so scrolling back is free.
// - createLimiter: the same cap for applies (one background op per book).
// - fillableFields: the 'Fill empty fields' half of the page's apply toggle
//   for the per-book apply endpoint, which takes a field list, not a mode.

import * as api from '../../../services/api';
import type { Book, BulkApplyMode, MetadataCandidate } from '../../../services/api';
import { submitStagedApply, type ToastFn } from '../../audiobooks/stagedMetadataApply';
import {
  METADATA_APPLY_FIELDS,
  candidateApplyFieldValue,
  type MetadataApplyField,
} from '../../../config/metadataApplyFields';

export const CANDIDATE_FETCH_CONCURRENCY = 4;
export const CANDIDATE_APPLY_CONCURRENCY = 4;

export interface CandidateQuery {
  title: string;
  author: string;
}

export type CandidateSearchFn = (
  bookId: string,
  query: CandidateQuery
) => Promise<MetadataCandidate[]>;

export type CandidateEntry =
  | { status: 'idle' }
  | { status: 'queued' }
  | { status: 'loading' }
  | { status: 'done'; results: MetadataCandidate[] }
  | { status: 'error'; error: string };

const IDLE: CandidateEntry = { status: 'idle' };
const QUEUED: CandidateEntry = { status: 'queued' };
const LOADING: CandidateEntry = { status: 'loading' };

export function candidateKey(bookId: string, q: CandidateQuery): string {
  return `${bookId}\u0000${q.title.trim()}\u0000${q.author.trim()}`;
}

interface Job {
  key: string;
  bookId: string;
  query: CandidateQuery;
}

export class CandidateLoader {
  private readonly entries = new Map<string, CandidateEntry>();
  private readonly listeners = new Map<string, Set<() => void>>();
  private queue: Job[] = [];
  private active = 0;

  constructor(
    private readonly search: CandidateSearchFn,
    private readonly maxConcurrent = CANDIDATE_FETCH_CONCURRENCY
  ) {}

  get(key: string): CandidateEntry {
    return this.entries.get(key) ?? IDLE;
  }

  /** Requests in flight right now; never above maxConcurrent. */
  get inFlight(): number {
    return this.active;
  }

  subscribe(key: string, fn: () => void): () => void {
    let set = this.listeners.get(key);
    if (!set) {
      set = new Set();
      this.listeners.set(key, set);
    }
    set.add(fn);
    return () => {
      set!.delete(fn);
      if (set!.size === 0) this.listeners.delete(key);
    };
  }

  /** Queue a search unless it is cached, running or already queued. */
  request(bookId: string, query: CandidateQuery): void {
    const key = candidateKey(bookId, query);
    const cur = this.get(key).status;
    if (cur === 'done' || cur === 'loading' || cur === 'queued') return;
    this.queue.push({ key, bookId, query });
    this.set(key, QUEUED);
    this.pump();
  }

  /** Drop a search that has not started yet (its card left the viewport). */
  cancel(bookId: string, query: CandidateQuery): void {
    const key = candidateKey(bookId, query);
    if (this.get(key).status !== 'queued') return;
    this.queue = this.queue.filter((j) => j.key !== key);
    this.set(key, IDLE);
  }

  private set(key: string, entry: CandidateEntry): void {
    this.entries.set(key, entry);
    this.listeners.get(key)?.forEach((fn) => fn());
  }

  private pump(): void {
    while (this.active < this.maxConcurrent && this.queue.length > 0) {
      const job = this.queue.shift()!;
      this.active++;
      this.set(job.key, LOADING);
      this.search(job.bookId, job.query)
        .then(
          (results) => this.set(job.key, { status: 'done', results }),
          (err: unknown) =>
            this.set(job.key, {
              status: 'error',
              error: err instanceof Error ? err.message : 'Search failed',
            })
        )
        .finally(() => {
          this.active--;
          this.pump();
        });
    }
  }
}

/** Runs at most `max` tasks at once; the rest wait their turn in order. */
export function createLimiter(max: number) {
  let active = 0;
  const waiting: Array<() => void> = [];
  const next = () => {
    if (active >= max) return;
    const start = waiting.shift();
    if (start) start();
  };
  return function run<T>(task: () => Promise<T>): Promise<T> {
    return new Promise<T>((resolve, reject) => {
      waiting.push(() => {
        active++;
        task()
          .then(resolve, reject)
          .finally(() => {
            active--;
            next();
          });
      });
      next();
    });
  };
}

export type Limiter = ReturnType<typeof createLimiter>;

/**
 * The book's current value for an apply field, or undefined when the Book
 * payload does not carry that field at all (then fill mode cannot know whether
 * it is empty and leaves it alone rather than risk overwriting it).
 */
function bookFieldValue(book: Book, field: MetadataApplyField): unknown | undefined {
  switch (field) {
    case 'title':
      return book.title;
    case 'author':
      return book.author_name || book.authors?.[0]?.name || '';
    case 'narrator':
      return book.narrator || book.narrators?.[0]?.name || '';
    case 'series':
      return book.series_name ?? '';
    case 'series_position':
      return book.series_position_raw || book.series_position || book.series_sequence || '';
    case 'year':
      return book.audiobook_release_year || book.print_year || '';
    case 'publisher':
      return book.publisher ?? '';
    case 'isbn':
      return book.isbn || book.isbn13 || book.isbn10 || '';
    case 'asin':
      return book.asin ?? '';
    case 'cover_url':
      return book.cover_url || book.cover_image || '';
    case 'description':
      return book.description ?? '';
    case 'language':
      return book.language ?? '';
    case 'genre':
      return book.genre ?? '';
    case 'duration_sec':
      return book.duration ?? '';
    default:
      // series_secondary, subtitle, abridged, page_count: not on the Book
      // payload, so their current value is unknown.
      return undefined;
  }
}

function isEmpty(v: unknown): boolean {
  return v === '' || v === null || v === 0 || (typeof v === 'string' && v.trim() === '');
}

/**
 * The fields 'Fill empty fields' may write: the candidate carries a value and
 * the book's current value is known and empty.
 */
export function fillableFields(book: Book, candidate: MetadataCandidate): MetadataApplyField[] {
  return METADATA_APPLY_FIELDS.filter((f) => {
    if (candidateApplyFieldValue(candidate, f) === undefined) return false;
    const cur = bookFieldValue(book, f);
    return cur !== undefined && isEmpty(cur);
  });
}

/**
 * Applies one candidate to one book through the Search Metadata dialog's
 * background apply (POST /audiobooks/:id/apply-metadata, background: true):
 * one op per book, an ASIN conflict offered back on the toast. 'fill' sends
 * only the fields the book has empty, read from the book as it is now;
 * 'replace' sends every field. Write-back on, as the dialog does.
 */
export async function applyCandidateToBook(args: {
  bookId: string;
  candidate: MetadataCandidate;
  mode: BulkApplyMode;
  toast: ToastFn;
  onApplied: () => void;
}): Promise<void> {
  const { bookId, candidate, mode, toast, onApplied } = args;
  let book: Book;
  try {
    book = await api.getBook(bookId);
  } catch (err) {
    toast(err instanceof Error ? err.message : 'Could not load that book', 'error');
    return;
  }
  let fields: string[] | undefined;
  if (mode === 'fill') {
    fields = fillableFields(book, candidate);
    if (fields.length === 0) {
      toast(
        `Nothing to fill on "${book.title}": every field this candidate carries is already set. Switch to Replace existing to overwrite.`,
        'info'
      );
      return;
    }
  }
  await submitStagedApply({
    book,
    pick: { candidate, fields },
    writeToFiles: true,
    toast,
    onApplied: () => onApplied(),
  });
}
