// file: web/src/components/review/spine/AuthorGroupSpine.memo.test.tsx
// version: 1.0.0
// guid: b7d41e92-5a06-4c38-8f1b-3e90a2c6d715
// last-edited: 2026-10-13
//
// Ticking one author-group checkbox must re-render ONE card, not the page -- and
// the memo that achieves that must not serve a stale card.
//
// The Authors analogue of DupesSpine.memo.test.tsx, and written for the same
// reason: a tick re-renders the workspace (the lane's state lives there), and a
// spine that handed every card the whole lane would repaint all 25-100 of them to
// show one tick.
//
// HOW THE RENDERS ARE COUNTED
//
// `hasMergeWork` is called exactly once per GroupCard render (it decides the
// Merge button), and nowhere else in the spine, so spying on it is a direct
// render counter that needs no instrumentation inside the component. Counting
// DOM nodes would not work: the point of a wasted re-render is that the output
// is identical.
//
// The two halves: the count half fails if the memo is removed (every card
// re-renders); the staleness half fails if the memo compares too little (the
// card that SHOULD change does not).

import { beforeEach, describe, expect, it, vi } from 'vitest';
import { render, screen, waitFor, within } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import * as api from '../../../services/api';
import * as lane from '../lanes/useAuthorsLane';
import * as reads from '../lanes/authorsReads';
import { AuthorGroupSpine } from './AuthorGroupSpine';

vi.mock('../../../services/api');
vi.mock('../lanes/authorsReads');

const hasMergeWorkSpy = vi.spyOn(lane, 'hasMergeWork');

const ROW_COUNT = 20;

// One toast for the whole file, as the ToastProvider gives the real workspace: a
// fresh `vi.fn()` per render would change `dispatch` every render and defeat the
// memo for a reason that exists only in the test.
const toast = vi.fn();
const NO_URL_FILTERS = { authorId: null };

function Harness() {
  const authors = lane.useAuthorsLane(toast, true, NO_URL_FILTERS);
  return <AuthorGroupSpine lane={authors} />;
}

beforeEach(() => {
  vi.resetAllMocks();
  hasMergeWorkSpy.mockClear();
  vi.mocked(api.getDedupCandidates).mockResolvedValue({ candidates: [], total: 0 });
  vi.mocked(reads.fetchAuthorGroups).mockResolvedValue({
    needsRefresh: false,
    groups: Array.from({ length: ROW_COUNT }, (_, i) => ({
      canonical: { id: i + 1, name: `Author ${i + 1}`, created_at: '' },
      variants: [
        { id: 100 + i, name: `Author ${i + 1}.`, created_at: '' },
        { id: 200 + i, name: `A. ${i + 1}`, created_at: '' },
      ],
      book_count: 2,
    })),
  });
});

async function renderSpine() {
  render(<Harness />);
  await screen.findByTestId('author-group-1');
  await waitFor(() => expect(screen.getAllByTestId(/^author-group-\d+$/)).toHaveLength(ROW_COUNT));
}

describe('AuthorGroupSpine memoization', () => {
  it('ticking one checkbox re-renders one card', async () => {
    const user = userEvent.setup();
    await renderSpine();
    hasMergeWorkSpy.mockClear();
    await user.click(screen.getByRole('checkbox', { name: 'Select group Author 7' }));
    expect(screen.getByRole('checkbox', { name: 'Select group Author 7' })).toBeChecked();
    expect(hasMergeWorkSpy).toHaveBeenCalledTimes(1);
  });

  it('leaving one variant out re-renders that card and shows it', async () => {
    const user = userEvent.setup();
    await renderSpine();
    hasMergeWorkSpy.mockClear();
    const card = screen.getByTestId('author-group-4');
    await user.click(within(card).getByRole('button', { name: 'Remove "Author 4." from this merge' }));
    // Not stale: the card that changed shows the change...
    expect(within(card).getByText(/1 variant\(s\) will be merged into it \(1 left out\)/)).toBeInTheDocument();
    // ...and only it was drawn again.
    expect(hasMergeWorkSpy).toHaveBeenCalledTimes(1);
  });

  it('moving the focus ring re-renders the two cards it touches', async () => {
    await renderSpine();
    expect(screen.getByTestId('author-group-1')).toHaveAttribute('data-focused', 'true');
    hasMergeWorkSpy.mockClear();
    const user = userEvent.setup();
    await user.keyboard('j');
    expect(screen.getByTestId('author-group-2')).toHaveAttribute('data-focused', 'true');
    expect(screen.getByTestId('author-group-1')).not.toHaveAttribute('data-focused');
    expect(hasMergeWorkSpy).toHaveBeenCalledTimes(2);
  });
});
