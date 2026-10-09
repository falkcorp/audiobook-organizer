// file: web/src/components/review/spine/CandidatesCard.test.tsx
// version: 1.3.0
// guid: e012200e-9c38-4a1d-8587-8ac43ce9803b
// last-edited: 2026-10-09

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
  vi.mocked(api.getConfig).mockResolvedValue({
    root_dir: '',
    path_aliases: [],
  } as unknown as Config);
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
    detailState: () => 'loaded',
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

    await act(async () =>
      d.resolve([cand('Low', 0.4), cand('Cached Pick', 0.8), cand('High', 0.95)])
    );
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
      expect(search).toHaveBeenCalledWith(
        'b1',
        { title: 'Book b1', author: 'Someone' },
        expect.any(Function)
      )
    );

    const title = screen.getByTestId('search-again-title');
    const author = screen.getByTestId('search-again-author');
    await user.clear(title);
    await user.type(title, 'The Real Title');
    await user.clear(author);
    await user.type(author, 'Real Author');
    await user.click(screen.getByRole('button', { name: 'Search again' }));

    await waitFor(() =>
      expect(search).toHaveBeenLastCalledWith(
        'b1',
        { title: 'The Real Title', author: 'Real Author', browse: true },
        expect.any(Function)
      )
    );
  });

  it('Search again with only an author lists every book the search returns, catalog first', async () => {
    const user = userEvent.setup();
    const full = deferred<MetadataCandidate[]>();
    const search = vi.fn(
      (_id: string, q: CandidateQuery, onPartial?: (r: MetadataCandidate[]) => void) => {
        if (!q.browse) return Promise.resolve([cached]);
        // The catalog answers first, the full browse search later.
        onPartial?.([
          { ...cand('Catalog Book One', 0.6), from_catalog: true },
          { ...cand('Catalog Book Two', 0.5), from_catalog: true },
        ]);
        return full.promise;
      }
    );
    const loader = new CandidateLoader(search);
    renderCards([row('b1')], { loader, apply: vi.fn() });
    await screen.findByText('Cached Pick');

    await user.clear(screen.getByTestId('search-again-title'));
    const author = screen.getByTestId('search-again-author');
    await user.clear(author);
    await user.type(author, 'joseph phelps');
    await user.click(screen.getByRole('button', { name: 'Search again' }));

    await waitFor(() =>
      expect(search).toHaveBeenLastCalledWith(
        'b1',
        { title: '', author: 'joseph phelps', browse: true },
        expect.any(Function)
      )
    );
    // The catalog's answer shows while the full search runs, in place of the
    // cached pick (the answer to a different question).
    await screen.findByText('Catalog Book One');
    expect(screen.getByText('Catalog Book Two')).toBeInTheDocument();
    expect(screen.queryByText('Cached Pick')).not.toBeInTheDocument();
    expect(screen.getAllByText('Catalog')).toHaveLength(2);

    await act(async () => {
      full.resolve([
        { ...cand('Catalog Book One', 0.6), from_catalog: true },
        { ...cand('Catalog Book Two', 0.5), from_catalog: true },
        cand('Live Book Three', 0.4),
        cand('Live Book Four', 0.3),
      ]);
    });
    await screen.findByText('Live Book Three');
    expect(
      within(screen.getByTestId('candidate-list')).getAllByTestId('candidate-item')
    ).toHaveLength(4);
  });

  it('Search again with the same text runs the search again', async () => {
    const user = userEvent.setup();
    const search = vi.fn((_id: string, _q: CandidateQuery) => Promise.resolve([cand('R', 0.7)]));
    const loader = new CandidateLoader(search);
    renderCards([row('b1')], { loader, apply: vi.fn() });
    await waitFor(() => expect(search).toHaveBeenCalledTimes(1));
    await user.click(screen.getByRole('button', { name: 'Search again' }));
    await waitFor(() => expect(search).toHaveBeenCalledTimes(2));
    await screen.findByText('R');
    await user.click(screen.getByRole('button', { name: 'Search again' }));
    await waitFor(() => expect(search).toHaveBeenCalledTimes(3));
    // ...and asks the server past its short-lived answer cache.
    expect(search).toHaveBeenLastCalledWith(
      'b1',
      { title: 'Book b1', author: 'Someone', browse: true },
      expect.any(Function),
      { refresh: true }
    );
  });

  it('Apply hands the chosen candidate to the panel; Reject of the cached pick is the lane reject', async () => {
    const user = userEvent.setup();
    const apply = vi.fn(() => Promise.resolve(false));
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
    await applyCandidateToBook({
      bookId: 'b1',
      candidate,
      mode: 'fill',
      toast: vi.fn(),
      onApplied: vi.fn(),
    });
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
    await applyCandidateToBook({
      bookId: 'b1',
      candidate,
      mode: 'replace',
      toast: vi.fn(),
      onApplied: vi.fn(),
    });
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
    await applyCandidateToBook({
      bookId: 'b1',
      candidate: full,
      mode: 'fill',
      toast,
      onApplied: vi.fn(),
    });
    expect(api.applyMetadataCandidate).not.toHaveBeenCalled();
    expect(toast).toHaveBeenCalledWith(expect.stringContaining('Nothing to fill'), 'info');
  });
});

describe('one apply per book', () => {
  it('disables every Apply in the card while one is running', async () => {
    const user = userEvent.setup();
    const gate = deferred<boolean>();
    const apply = vi.fn(() => gate.promise);
    const loader = new CandidateLoader(() => Promise.resolve([cand('High', 0.95), cached]));
    renderCards([row('b1')], { loader, apply });
    await screen.findByText('High');
    const buttons = () => screen.getAllByRole('button', { name: 'Apply' });
    await user.click(buttons()[0]);
    expect(apply).toHaveBeenCalledTimes(1);
    buttons().forEach((b) => expect(b).toBeDisabled());
    await act(async () => gate.resolve(false));
    buttons().forEach((b) => expect(b).not.toBeDisabled());
  });
});

describe('Not the best match (thumbs-down)', () => {
  function setup(applyResult = true) {
    const user = userEvent.setup();
    const results = [cand('High', 0.95), cand('Cached Pick', 0.8), cand('Low', 0.4)];
    const loader = new CandidateLoader(() => Promise.resolve(results));
    const apply = vi.fn(() => Promise.resolve(applyResult));
    const spineCtx = ctx();
    vi.mocked(api.recordCandidateFeedback).mockReset();
    vi.mocked(api.deleteCandidateFeedback).mockReset();
    vi.mocked(api.recordCandidateFeedback).mockResolvedValue({ id: 'b1:q:c', label: 'negative' });
    vi.mocked(api.deleteCandidateFeedback).mockResolvedValue({ removed: true });
    renderCards([row('b1')], { loader, apply }, spineCtx);
    return { user, apply, spineCtx };
  }

  const items = () => within(screen.getByTestId('candidate-list')).getAllByTestId('candidate-item');
  const itemFor = (title: string) => items().find((el) => within(el).queryByText(title))!;

  it('records a negative label with the query, marks the row, and neither hides nor rejects', async () => {
    const { user, spineCtx } = setup();
    await waitFor(() => expect(items()).toHaveLength(3));

    const low = itemFor('Low');
    await user.click(within(low).getByRole('button', { name: 'Not the best match' }));

    await waitFor(() => expect(api.recordCandidateFeedback).toHaveBeenCalledTimes(1));
    expect(api.recordCandidateFeedback).toHaveBeenCalledWith({
      book_id: 'b1',
      label: 'negative',
      query: { title: 'Book b1', author: 'Someone', browse: false },
      candidate: expect.objectContaining({ title: 'Low', source: 'audible', score: 0.4 }),
      rank: 3,
      result_count: 3,
    });
    expect(items()).toHaveLength(3);
    expect(itemFor('Low')).toHaveAttribute('data-thumbs-down', 'true');
    expect(within(itemFor('Low')).getByText('Not the best match')).toBeInTheDocument();
    expect(itemFor('High')).not.toHaveAttribute('data-thumbs-down');
    expect(spineCtx.onAction).not.toHaveBeenCalled();
  });

  it('a second click undoes it with a DELETE for the negative label', async () => {
    const { user, spineCtx } = setup();
    await waitFor(() => expect(items()).toHaveLength(3));
    const button = () => within(itemFor('Low')).getByRole('button', { name: 'Not the best match' });

    await user.click(button());
    await waitFor(() => expect(button()).not.toBeDisabled());
    await user.click(button());

    await waitFor(() =>
      expect(api.deleteCandidateFeedback).toHaveBeenCalledWith('b1:q:c', 'negative')
    );
    expect(itemFor('Low')).not.toHaveAttribute('data-thumbs-down');
    expect(items()).toHaveLength(3);
    expect(spineCtx.onAction).not.toHaveBeenCalled();
  });

  it('drops the mark when the server refuses the label', async () => {
    const { user } = setup();
    vi.mocked(api.recordCandidateFeedback).mockRejectedValue(new Error('nope'));
    await waitFor(() => expect(items()).toHaveLength(3));
    await user.click(within(itemFor('Low')).getByRole('button', { name: 'Not the best match' }));
    expect(await screen.findByText('nope')).toBeInTheDocument();
    expect(itemFor('Low')).not.toHaveAttribute('data-thumbs-down');
  });

  it('records the applied candidate as the positive once the apply lands', async () => {
    const { user, apply } = setup(true);
    await waitFor(() => expect(items()).toHaveLength(3));
    await user.click(within(itemFor('High')).getByRole('button', { name: 'Apply' }));

    await waitFor(() => expect(api.recordCandidateFeedback).toHaveBeenCalledTimes(1));
    expect(apply).toHaveBeenCalledWith('b1', expect.objectContaining({ title: 'High' }));
    expect(api.recordCandidateFeedback).toHaveBeenCalledWith(
      expect.objectContaining({
        label: 'positive',
        candidate: expect.objectContaining({ title: 'High' }),
        rank: 1,
      })
    );
  });

  it('records no positive when the apply did not land', async () => {
    const { user, apply } = setup(false);
    await waitFor(() => expect(items()).toHaveLength(3));
    await user.click(within(itemFor('High')).getByRole('button', { name: 'Apply' }));
    await waitFor(() => expect(apply).toHaveBeenCalled());
    await act(async () => {});
    expect(api.recordCandidateFeedback).not.toHaveBeenCalled();
  });
});
