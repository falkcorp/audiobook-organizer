// file: web/src/components/review/spine/CandidatesCard.test.tsx
// version: 1.0.1
// guid: e012200e-9c38-4a1d-8587-8ac43ce9803b
// last-edited: 2026-10-07

import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import { act, render, screen, waitFor, within } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { ThemeProvider } from '@mui/material/styles';
import { appTheme } from '../../../theme';
import * as api from '../../../services/api';
import type { Book, CandidateResult, Config, MetadataCandidate } from '../../../services/api';
import { CompareSpine, type SpineContext } from './CompareSpine';
import {
  CANDIDATE_FETCH_CONCURRENCY,
  CandidateLoader,
  applyCandidateToBook,
  createLimiter,
  type CandidateQuery,
} from './candidateLoader';
import type { CandidatesContext } from './CandidatesCard';

vi.mock('../../../services/api');

// jsdom has no layout, so cards never intersect on their own. This observer
// reports every observed element as on screen, which is what "the reviewer
// scrolled these cards into view" looks like to the card.
const realIO = globalThis.IntersectionObserver;
beforeEach(() => {
  vi.mocked(api.getConfig).mockResolvedValue({ root_dir: '', path_aliases: [] } as unknown as Config);
  globalThis.IntersectionObserver = class {
    constructor(private cb: IntersectionObserverCallback) {}
    observe(el: Element) {
      this.cb(
        [{ isIntersecting: true, target: el } as IntersectionObserverEntry],
        this as unknown as IntersectionObserver
      );
    }
    unobserve() {}
    disconnect() {}
    takeRecords() {
      return [];
    }
  } as unknown as typeof IntersectionObserver;
});
afterEach(() => {
  globalThis.IntersectionObserver = realIO;
});

function cand(title: string, score: number, source = 'audible'): MetadataCandidate {
  return { title, author: 'A. Writer', source, score } as unknown as MetadataCandidate;
}

const cached = cand('Cached Pick', 0.8);

function row(id: string): CandidateResult {
  return {
    book: { id, title: `Book ${id}`, author: 'Someone', file_path: `/audio/${id}.m4b` },
    candidate: cached,
    status: 'matched',
  };
}

function ctx(): SpineContext {
  return {
    rowState: () => undefined,
    isSelected: () => false,
    onToggleSelect: vi.fn(),
    onPreviewCover: vi.fn(),
    onAction: vi.fn(),
    expandedId: null,
    onToggleExpand: vi.fn(),
  };
}

function deferred<T>() {
  let resolve!: (v: T) => void;
  const promise = new Promise<T>((r) => (resolve = r));
  return { promise, resolve };
}

function renderCards(rows: CandidateResult[], cands: CandidatesContext, spineCtx = ctx()) {
  return render(
    <ThemeProvider theme={appTheme} defaultMode="dark">
      <CompareSpine rows={rows} viewMode="candidates" ctx={spineCtx} candidates={cands} />
    </ThemeProvider>
  );
}

describe('CandidateLoader', () => {
  it('never runs more than 4 searches at once, and starts the next as one finishes', async () => {
    const pending: Array<ReturnType<typeof deferred<MetadataCandidate[]>>> = [];
    const search = vi.fn(() => {
      const d = deferred<MetadataCandidate[]>();
      pending.push(d);
      return d.promise;
    });
    const loader = new CandidateLoader(search);
    const q: CandidateQuery = { title: 't', author: 'a' };
    for (let i = 0; i < 10; i++) loader.request(`b${i}`, q);
    expect(CANDIDATE_FETCH_CONCURRENCY).toBe(4);
    expect(search).toHaveBeenCalledTimes(4);
    expect(loader.inFlight).toBe(4);

    await act(async () => pending[0].resolve([]));
    expect(search).toHaveBeenCalledTimes(5);
    expect(loader.inFlight).toBe(4);
  });

  it('drops a queued search whose card left the viewport, and memoizes answers', async () => {
    const search = vi.fn(() => Promise.resolve([cand('X', 0.5)]));
    const loader = new CandidateLoader(search, 1);
    const q: CandidateQuery = { title: 't', author: '' };
    loader.request('b1', q);
    loader.request('b2', q);
    loader.cancel('b2', q);
    await waitFor(() => expect(loader.inFlight).toBe(0));
    expect(search).toHaveBeenCalledTimes(1);
    loader.request('b1', q); // cached: no second request
    expect(search).toHaveBeenCalledTimes(1);
  });

  it('createLimiter caps concurrent tasks', async () => {
    const run = createLimiter(4);
    let running = 0;
    let peak = 0;
    const gates = Array.from({ length: 6 }, () => deferred<void>());
    const all = gates.map((g) =>
      run(async () => {
        running++;
        peak = Math.max(peak, running);
        await g.promise;
        running--;
      })
    );
    await Promise.resolve();
    expect(running).toBe(4);
    gates.forEach((g) => g.resolve());
    await Promise.all(all);
    expect(peak).toBe(4);
  });
});

describe('Candidates view', () => {
  it('shows the cached pick at once, then the full ranked list', async () => {
    const d = deferred<MetadataCandidate[]>();
    const loader = new CandidateLoader(() => d.promise);
    renderCards([row('b1')], { loader, apply: vi.fn() });

    const list = screen.getByTestId('candidate-list');
    expect(within(list).getByText('Cached Pick')).toBeInTheDocument();
    expect(screen.getByText('Searching…')).toBeInTheDocument();

    await act(async () => d.resolve([cand('Low', 0.4), cand('Cached Pick', 0.8), cand('High', 0.95)]));
    const titles = within(list)
      .getAllByTestId('candidate-item')
      .map((el) => el.querySelector('.MuiTypography-body2')?.textContent);
    expect(titles).toEqual(['High', 'Cached Pick', 'Low']);
    expect(within(list).getAllByText('How this score was reached')).toHaveLength(3);
  });

  it('loads at most 4 cards at a time across the page', async () => {
    const search = vi.fn(() => new Promise<MetadataCandidate[]>(() => {}));
    const loader = new CandidateLoader(search);
    renderCards(
      Array.from({ length: 10 }, (_, i) => row(`b${i}`)),
      { loader, apply: vi.fn() }
    );
    await waitFor(() => expect(search).toHaveBeenCalledTimes(4));
    expect(screen.getAllByText('Waiting to search…')).toHaveLength(6);
  });

  it('Search again retargets the query with the edited title and author', async () => {
    const user = userEvent.setup();
    const search = vi.fn((_id: string, _q: CandidateQuery) => Promise.resolve([cand('R', 0.7)]));
    const loader = new CandidateLoader(search);
    renderCards([row('b1')], { loader, apply: vi.fn() });
    await waitFor(() =>
      expect(search).toHaveBeenCalledWith('b1', { title: 'Book b1', author: 'Someone' })
    );

    const title = screen.getByTestId('search-again-title');
    const author = screen.getByTestId('search-again-author');
    await user.clear(title);
    await user.type(title, 'The Real Title');
    await user.clear(author);
    await user.type(author, 'Real Author');
    await user.click(screen.getByRole('button', { name: 'Search again' }));

    await waitFor(() =>
      expect(search).toHaveBeenLastCalledWith('b1', {
        title: 'The Real Title',
        author: 'Real Author',
      })
    );
  });

  it('Apply hands the chosen candidate to the panel; Reject of the cached pick is the lane reject', async () => {
    const user = userEvent.setup();
    const apply = vi.fn(() => Promise.resolve());
    const loader = new CandidateLoader(() => Promise.resolve([cand('High', 0.95), cached]));
    const spineCtx = ctx();
    renderCards([row('b1')], { loader, apply }, spineCtx);
    await screen.findByText('High');

    const items = screen.getAllByTestId('candidate-item');
    await user.click(within(items[0]).getByRole('button', { name: 'Apply' }));
    expect(apply).toHaveBeenCalledWith('b1', expect.objectContaining({ title: 'High' }));

    await user.click(within(items[1]).getByRole('button', { name: 'Reject' }));
    expect(spineCtx.onAction).toHaveBeenCalledWith({ lane: 'metadata', type: 'reject', id: 'b1' });

    // A non-cached candidate has no server-side reject: it is hidden here.
    await user.click(within(items[0]).getByRole('button', { name: 'Reject' }));
    expect(screen.queryByText('High')).not.toBeInTheDocument();
  });
});

describe('applyCandidateToBook', () => {
  const book = {
    id: 'b1',
    title: 'Book',
    author_name: 'Set Author',
    narrator: '',
    file_path: '/x',
    created_at: '',
    updated_at: '',
  } as unknown as Book;
  const candidate = {
    title: 'New Title',
    author: 'New Author',
    narrator: 'New Narrator',
    source: 'audible',
    score: 0.9,
  } as unknown as MetadataCandidate;

  beforeEach(() => {
    vi.mocked(api.applyMetadataCandidate).mockClear();
    vi.mocked(api.getBook).mockResolvedValue(book);
    vi.mocked(api.applyMetadataCandidate).mockResolvedValue({
      message: '',
      book,
      source: 'audible',
      operation_id: 'op1',
    } as unknown as Awaited<ReturnType<typeof api.applyMetadataCandidate>>);
    vi.mocked(api.pollOperationV2).mockResolvedValue({ status: 'completed' } as never);
  });

  it('fill mode sends only the empty fields, as a background per-book apply', async () => {
    await applyCandidateToBook({ bookId: 'b1', candidate, mode: 'fill', toast: vi.fn(), onApplied: vi.fn() });
    expect(api.applyMetadataCandidate).toHaveBeenCalledWith(
      'b1',
      candidate,
      ['narrator'],
      true,
      undefined,
      { background: true }
    );
  });

  it('replace mode sends every field', async () => {
    await applyCandidateToBook({ bookId: 'b1', candidate, mode: 'replace', toast: vi.fn(), onApplied: vi.fn() });
    expect(api.applyMetadataCandidate).toHaveBeenCalledWith(
      'b1',
      candidate,
      undefined,
      true,
      undefined,
      { background: true }
    );
  });

  it('fill mode with nothing empty applies nothing and says so', async () => {
    const toast = vi.fn();
    const full = { ...candidate, narrator: undefined } as unknown as MetadataCandidate;
    await applyCandidateToBook({ bookId: 'b1', candidate: full, mode: 'fill', toast, onApplied: vi.fn() });
    expect(api.applyMetadataCandidate).not.toHaveBeenCalled();
    expect(toast).toHaveBeenCalledWith(expect.stringContaining('Nothing to fill'), 'info');
  });
});

describe('one apply per book', () => {
  it('disables every Apply in the card while one is running', async () => {
    const user = userEvent.setup();
    const gate = deferred<void>();
    const apply = vi.fn(() => gate.promise);
    const loader = new CandidateLoader(() => Promise.resolve([cand('High', 0.95), cached]));
    renderCards([row('b1')], { loader, apply });
    await screen.findByText('High');
    const buttons = () => screen.getAllByRole('button', { name: 'Apply' });
    await user.click(buttons()[0]);
    expect(apply).toHaveBeenCalledTimes(1);
    buttons().forEach((b) => expect(b).toBeDisabled());
    await act(async () => gate.resolve());
    buttons().forEach((b) => expect(b).not.toBeDisabled());
  });
});
