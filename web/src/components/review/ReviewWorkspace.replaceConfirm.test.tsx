// file: web/src/components/review/ReviewWorkspace.replaceConfirm.test.tsx
// version: 1.2.0
// guid: 510b281d-e333-4300-8b36-49e93cd8a7aa
// last-edited: 2026-09-27
//
// Owner decision on #3580: with the bulk toggle on "Replace existing", EVERY
// bulk entry point -- the action bar's buttons, a group's Apply All, and the
// workspace's "Apply selected fields" / "Apply all fields" -- confirms exactly
// once (the lane's applySelected dispatch asks, nothing else does), naming the
// book count. Cancel sends no request. On "Fill empty fields" nothing asks.
//
// Owner request 2026-09-27: the prompt is an MUI dialog with "Don't ask me
// again". Ticked + Replace persists a per-viewer flag and later Replace bulk
// applies go straight through; ticked + Cancel persists nothing; the action
// bar's "Ask before replacing again" clears it; blocked storage keeps asking.

import { render, screen, waitFor, within } from '@testing-library/react';
import { MemoryRouter } from 'react-router-dom';
import userEvent from '@testing-library/user-event';
import { vi, describe, it, expect, beforeEach, afterEach } from 'vitest';
import * as api from '../../services/api';
import { ReviewWorkspace } from './ReviewWorkspace';
import { ToastProvider } from '../toast/ToastProvider';
import { STORAGE_KEYS } from '../../lib/storageKeys';

vi.mock('../../services/api');

function makeResult(id: string, candTitle = `Cand ${id}`) {
  return {
    book: { id, title: `Book ${id}`, language: 'en' },
    status: 'matched',
    candidate_hash: `h-${id}`,
    candidate: {
      source: 'audible',
      title: candTitle,
      author: 'A',
      narrator: 'N',
      score: 2.0,
      language: 'en',
    },
  } as unknown as api.CandidateResult;
}

function seed(results: api.CandidateResult[]) {
  vi.mocked(api.getCachedReviewResults).mockResolvedValue({
    results,
    total_count: results.length,
    matched: results.length,
    no_match: 0,
    errors: 0,
    stale: 0,
  } as unknown as Awaited<ReturnType<typeof api.getCachedReviewResults>>);
  vi.mocked(api.getDedupCandidates).mockResolvedValue({ candidates: [], total: 0 });
  vi.mocked(api.getDedupStats).mockResolvedValue({ stats: [] });
  vi.mocked(api.getReviewItems).mockResolvedValue({
    items: [],
    count: 0,
    limit: 500,
    offset: 0,
    total: 0,
  });
  vi.mocked(api.getReviewCount).mockResolvedValue({ count: 0, by_kind: {} });
  vi.mocked(api.getConfig).mockResolvedValue({ root_dir: '' } as api.Config);
  vi.mocked(api.batchApplyFromCache).mockResolvedValue({
    op_id: 'op-1',
  } as Awaited<ReturnType<typeof api.batchApplyFromCache>>);
  vi.mocked(api.pollOperationV2).mockReturnValue(
    new Promise(() => {}) as ReturnType<typeof api.pollOperationV2>
  );
}

async function openWorkspace(mode: api.BulkApplyMode | null, results: api.CandidateResult[]) {
  if (mode) window.localStorage.setItem(STORAGE_KEYS.METADATA_REVIEW_BULK_APPLY_MODE, mode);
  seed(results);
  return renderWorkspace();
}

async function renderWorkspace() {
  const view = render(
    <MemoryRouter initialEntries={['/review']}>
      <ToastProvider>
        <ReviewWorkspace />
      </ToastProvider>
    </MemoryRouter>
  );
  await waitFor(() => expect(screen.getByTestId('compare-spine')).toBeInTheDocument());
  return view;
}

type User = ReturnType<typeof userEvent.setup>;

/** Each entry point: the rows it needs, and how to click it. Every one sends 2 books. */
const entryPoints: Array<{
  name: string;
  results: () => api.CandidateResult[];
  click: (user: User) => Promise<void>;
}> = [
  {
    name: 'action bar Apply page',
    results: () => [makeResult('a'), makeResult('b')],
    click: async (user) => user.click(await screen.findByTestId('apply-page')),
  },
  {
    name: 'group Apply All',
    // Same candidate on both books: one multi-book group card.
    results: () => [makeResult('a', 'Shared'), makeResult('b', 'Shared')],
    click: async (user) => user.click(await screen.findByTestId('group-apply-all')),
  },
  {
    name: 'workspace Apply selected fields',
    results: () => [makeResult('a'), makeResult('b')],
    click: async (user) => {
      const boxes = within(screen.getByTestId('compare-spine')).getAllByRole('checkbox');
      await user.click(boxes[0]);
      await user.click(boxes[1]);
      await user.click(screen.getByTestId('command-menu-metadata'));
      await user.click(await screen.findByTestId('command-apply-selected-fields'));
    },
  },
  {
    name: 'workspace Apply all fields',
    results: () => [makeResult('a'), makeResult('b')],
    click: async (user) => {
      await user.click(screen.getByTestId('command-menu-metadata'));
      await user.click(await screen.findByTestId('command-apply-all-fields'));
    },
  },
];

const dialog = () => screen.queryByTestId('replace-confirm-dialog');

async function answer(user: User, choice: 'accept' | 'cancel', dontAskAgain = false) {
  const d = await screen.findByTestId('replace-confirm-dialog');
  if (dontAskAgain) {
    await user.click(within(d).getByRole('checkbox', { name: "Don't ask me again" }));
  }
  await user.click(within(d).getByTestId(`replace-confirm-${choice}`));
  await waitFor(() => expect(dialog()).not.toBeInTheDocument());
}

/** Tick row `index`'s checkbox and run the action bar's Apply selected. */
async function applySelectedRow(user: User, index: number) {
  const boxes = within(screen.getByTestId('compare-spine')).getAllByRole('checkbox');
  await user.click(boxes[index]);
  await user.click(screen.getByTestId('apply-selected'));
}

const skipFlag = () =>
  window.localStorage.getItem(STORAGE_KEYS.METADATA_REVIEW_SKIP_REPLACE_CONFIRM);

beforeEach(() => {
  vi.resetAllMocks();
  window.localStorage.clear();
  // Review level Off: the default (In-depth) hides multi-book matches, which
  // is exactly the group the 'group Apply All' entry point needs on screen.
  window.localStorage.setItem(STORAGE_KEYS.METADATA_REVIEW_LEVEL, 'off');
});

afterEach(() => {
  vi.restoreAllMocks();
});

describe.each(entryPoints)('$name', ({ results, click }) => {
  it('Replace: prompts once naming the count, then applies with mode replace', async () => {
    const user = userEvent.setup();
    await openWorkspace('replace', results());
    await click(user);

    const d = await screen.findByTestId('replace-confirm-dialog');
    expect(screen.getAllByTestId('replace-confirm-dialog')).toHaveLength(1);
    const message = within(d).getByTestId('replace-confirm-message');
    expect(message).toHaveTextContent('2 book(s)');
    expect(message).toHaveTextContent('REPLACE existing values');
    expect(within(d).getByTestId('replace-confirm-accept')).toHaveTextContent('Replace 2 books');
    // Nothing is sent while the prompt is open.
    expect(api.batchApplyFromCache).not.toHaveBeenCalled();

    await answer(user, 'accept');
    await waitFor(() => expect(api.batchApplyFromCache).toHaveBeenCalledTimes(1));
    expect(vi.mocked(api.batchApplyFromCache).mock.calls[0][3]).toBe('replace');
    // An unticked box stores nothing.
    expect(skipFlag()).toBeNull();
  });

  it('Replace: cancel sends no request', async () => {
    const user = userEvent.setup();
    await openWorkspace('replace', results());
    await click(user);
    await answer(user, 'cancel');

    expect(api.batchApplyFromCache).not.toHaveBeenCalled();
  });

  it('Fill: no prompt, applies with mode fill', async () => {
    const user = userEvent.setup();
    await openWorkspace('fill', results());
    await click(user);

    await waitFor(() => expect(api.batchApplyFromCache).toHaveBeenCalledTimes(1));
    expect(dialog()).not.toBeInTheDocument();
    expect(vi.mocked(api.batchApplyFromCache).mock.calls[0][3]).toBe('fill');
  });
});

describe("Replace prompt: Don't ask me again", () => {
  it('ticked + Replace: the next Replace bulk apply sends without a prompt, also after a remount', async () => {
    const user = userEvent.setup();
    const view = await openWorkspace('replace', [
      makeResult('a'),
      makeResult('b'),
      makeResult('c'),
    ]);

    await applySelectedRow(user, 0);
    await answer(user, 'accept', true);
    await waitFor(() => expect(api.batchApplyFromCache).toHaveBeenCalledTimes(1));
    expect(skipFlag()).toBe('true');
    expect(screen.getByTestId('reset-replace-confirm')).toBeInTheDocument();

    // Row a is now applied and hidden by the default filter; b and c remain.
    await waitFor(() =>
      expect(within(screen.getByTestId('compare-spine')).getAllByRole('checkbox')).toHaveLength(2)
    );
    await applySelectedRow(user, 0);
    await waitFor(() => expect(api.batchApplyFromCache).toHaveBeenCalledTimes(2));
    expect(dialog()).not.toBeInTheDocument();
    expect(vi.mocked(api.batchApplyFromCache).mock.calls[1][0]).toEqual(['b']);
    expect(vi.mocked(api.batchApplyFromCache).mock.calls[1][3]).toBe('replace');

    // A fresh mount (a reload) reads the stored flag: still no prompt.
    view.unmount();
    vi.mocked(api.batchApplyFromCache).mockClear();
    await renderWorkspace();
    await user.click(await screen.findByTestId('apply-page'));
    await waitFor(() => expect(api.batchApplyFromCache).toHaveBeenCalledTimes(1));
    expect(dialog()).not.toBeInTheDocument();
    expect(vi.mocked(api.batchApplyFromCache).mock.calls[0][3]).toBe('replace');
  });

  it('ticked + Cancel persists nothing: the next Replace still prompts', async () => {
    const user = userEvent.setup();
    await openWorkspace('replace', [makeResult('a'), makeResult('b')]);

    await user.click(await screen.findByTestId('apply-page'));
    await answer(user, 'cancel', true);
    expect(api.batchApplyFromCache).not.toHaveBeenCalled();
    expect(skipFlag()).toBeNull();
    expect(screen.queryByTestId('reset-replace-confirm')).not.toBeInTheDocument();

    await user.click(screen.getByTestId('apply-page'));
    expect(await screen.findByTestId('replace-confirm-dialog')).toBeInTheDocument();
    expect(api.batchApplyFromCache).not.toHaveBeenCalled();
  });

  it('"Ask before replacing again" clears the flag and brings the prompt back', async () => {
    window.localStorage.setItem(STORAGE_KEYS.METADATA_REVIEW_SKIP_REPLACE_CONFIRM, 'true');
    const user = userEvent.setup();
    await openWorkspace('replace', [makeResult('a'), makeResult('b')]);

    await user.click(await screen.findByTestId('reset-replace-confirm'));
    expect(screen.queryByTestId('reset-replace-confirm')).not.toBeInTheDocument();
    expect(skipFlag()).toBeNull();

    await user.click(screen.getByTestId('apply-page'));
    expect(await screen.findByTestId('replace-confirm-dialog')).toBeInTheDocument();
    expect(api.batchApplyFromCache).not.toHaveBeenCalled();
  });

  it('blocked storage: the prompt still appears, and ticking the box does not stop the next one', async () => {
    const key = STORAGE_KEYS.METADATA_REVIEW_SKIP_REPLACE_CONFIRM;
    // Block only the skip flag's key: other workspace reads are unguarded, and
    // the mode toggle must still work to reach Replace. Spied on the instance,
    // not Storage.prototype: test/setup.ts installs a plain-object
    // localStorage, so a prototype spy would never be reached.
    const store = window.localStorage;
    const realGet = store.getItem.bind(store);
    const realSet = store.setItem.bind(store);
    const getSpy = vi.spyOn(store, 'getItem').mockImplementation((k: string) => {
      if (k === key) throw new DOMException('blocked', 'SecurityError');
      return realGet(k);
    });
    const setSpy = vi.spyOn(store, 'setItem').mockImplementation((k: string, v: string) => {
      if (k === key) throw new DOMException('blocked', 'SecurityError');
      return realSet(k, v);
    });

    const user = userEvent.setup();
    await openWorkspace(null, [makeResult('a'), makeResult('b'), makeResult('c')]);
    await user.click(screen.getByTestId('bulk-apply-mode-replace'));

    await applySelectedRow(user, 0);
    await answer(user, 'accept', true);
    await waitFor(() => expect(api.batchApplyFromCache).toHaveBeenCalledTimes(1));
    expect(vi.mocked(api.batchApplyFromCache).mock.calls[0][3]).toBe('replace');
    // The block was actually reached, on both the read and the write.
    expect(getSpy).toHaveBeenCalledWith(key);
    expect(setSpy).toHaveBeenCalledWith(key, 'true');
    expect(screen.queryByTestId('reset-replace-confirm')).not.toBeInTheDocument();

    await waitFor(() =>
      expect(within(screen.getByTestId('compare-spine')).getAllByRole('checkbox')).toHaveLength(2)
    );
    await applySelectedRow(user, 0);
    expect(await screen.findByTestId('replace-confirm-dialog')).toBeInTheDocument();
    expect(api.batchApplyFromCache).toHaveBeenCalledTimes(1);
  });
});
