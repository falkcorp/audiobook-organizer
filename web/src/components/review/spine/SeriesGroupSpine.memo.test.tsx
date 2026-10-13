// file: web/src/components/review/spine/SeriesGroupSpine.memo.test.tsx
// version: 1.0.0
// guid: 9a4c7e21-b385-4d06-8f1e-52c0d7a93b64
// last-edited: 2026-10-13
//
// Ticking one series-group checkbox must re-render ONE card, not the page -- and
// the memo that achieves that must not serve a stale card.
//
// The Series analogue of AuthorGroupSpine.memo.test.tsx, written for the same
// reason: a tick re-renders the workspace (the lane's state lives there), and a
// spine that handed every card the whole lane, or a callback that changed
// identity on each render, would repaint all 25-100 of them to show one tick.
//
// HOW THE RENDERS ARE COUNTED
//
// `keptRowOf` is called exactly once per SeriesCard render (it picks the Merge
// button's target) and nowhere else in the spine, so spying on it is a direct
// render counter that needs no instrumentation inside the component. Counting DOM
// nodes would not work: the point of a wasted re-render is that the output is
// identical.
//
// The two halves: the count half fails if the memo is removed or a handler loses
// its stable identity (every card re-renders); the staleness half fails if the
// memo compares too little (the card that SHOULD change does not).

import { beforeEach, describe, expect, it, vi } from 'vitest';
import { render, screen, waitFor, within } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import * as api from '../../../services/api';
import * as lane from '../lanes/useAuthorsLane';
import * as reads from '../lanes/authorsReads';
import * as groups from '../lanes/seriesGroups';
import { SeriesGroupSpine } from './SeriesGroupSpine';

vi.mock('../../../services/api');
vi.mock('../lanes/authorsReads');

const keptRowSpy = vi.spyOn(groups, 'keptRowOf');

const ROW_COUNT = 20;

// One toast for the whole file, as the ToastProvider gives the real workspace: a
// fresh `vi.fn()` per render would change `dispatch` every render and defeat the
// memo for a reason that exists only in the test.
const toast = vi.fn();
const URL_FILTERS = { authorId: null, view: 'series' };

function Harness() {
  const authors = lane.useAuthorsLane(toast, true, URL_FILTERS);
  return <SeriesGroupSpine lane={authors} />;
}

beforeEach(() => {
  vi.resetAllMocks();
  keptRowSpy.mockClear();
  vi.mocked(reads.fetchSeriesGroups).mockResolvedValue({
    needsRefresh: false,
    totalSeries: ROW_COUNT * 2,
    groups: Array.from({ length: ROW_COUNT }, (_, i) => ({
      name: `Series ${i + 1}`,
      count: 2,
      match_type: 'exact',
      series: [
        { id: (i + 1) * 10 + 1, name: `Series ${i + 1}`, created_at: '' },
        { id: (i + 1) * 10 + 2, name: `series ${i + 1}`, created_at: '', author_id: i + 1, author_name: `Author ${i + 1}` },
      ],
    })),
  });
});

async function renderSpine() {
  render(<Harness />);
  await screen.findByTestId('series-group-s11-12');
  await waitFor(() => expect(screen.getAllByTestId(/^series-group-s\d/)).toHaveLength(ROW_COUNT));
}

describe('SeriesGroupSpine memoization', () => {
  it('ticking one checkbox re-renders one card', async () => {
    const user = userEvent.setup();
    await renderSpine();
    keptRowSpy.mockClear();
    await user.click(screen.getByRole('checkbox', { name: 'Select group Series 7' }));
    expect(screen.getByRole('checkbox', { name: 'Select group Series 7' })).toBeChecked();
    expect(keptRowSpy).toHaveBeenCalledTimes(1);
  });

  it('leaving one series out re-renders that card and shows it', async () => {
    const user = userEvent.setup();
    await renderSpine();
    keptRowSpy.mockClear();
    const card = screen.getByTestId('series-group-s41-42');
    await user.click(within(card).getByRole('checkbox', { name: 'Include series Series 4 (ID 41) in the merge' }));
    // Not stale: the card that changed shows the change...
    expect(within(card).getByTestId('series-row-41')).toHaveTextContent('Left out');
    expect(within(card).getByRole('button', { name: /^Merge into/ })).toBeDisabled();
    // ...and only it was drawn again.
    expect(keptRowSpy).toHaveBeenCalledTimes(1);
  });

  it('flagging a narrator re-renders that card and shows it', async () => {
    const user = userEvent.setup();
    await renderSpine();
    keptRowSpy.mockClear();
    const card = screen.getByTestId('series-group-s51-52');
    await user.click(within(card).getByText('Author', { exact: true }));
    expect(within(card).getByText('Narrator')).toBeInTheDocument();
    expect(keptRowSpy).toHaveBeenCalledTimes(1);
  });

  it('opening a name editor re-renders that card only', async () => {
    const user = userEvent.setup();
    await renderSpine();
    keptRowSpy.mockClear();
    const card = screen.getByTestId('series-group-s61-62');
    await user.click(within(card).getByRole('button', { name: 'Edit series name Series 6' }));
    expect(within(card).getByRole('textbox', { name: 'Series name' })).toBeInTheDocument();
    expect(keptRowSpy).toHaveBeenCalledTimes(1);
  });

  it('a rename repaints the card it concerns, not the others, and offers its Undo', async () => {
    const user = userEvent.setup();
    vi.mocked(api.renameSeries).mockResolvedValue({ id: 'rename-op-1' } as api.Operation);
    await renderSpine();
    const card = screen.getByTestId('series-group-s81-82');
    await user.click(within(card).getByRole('button', { name: 'Edit series name Series 8' }));
    await user.type(within(card).getByRole('textbox', { name: 'Series name' }), ' Edited{Enter}');
    await within(card).findByRole('button', { name: /Undo rename/ });
    keptRowSpy.mockClear();

    // With an undo on screen, ticking ANOTHER card still draws exactly one card.
    await user.click(screen.getByRole('checkbox', { name: 'Select group Series 9' }));
    expect(keptRowSpy).toHaveBeenCalledTimes(1);
  });

  it('moving the focus ring re-renders the two cards it touches', async () => {
    await renderSpine();
    expect(screen.getByTestId('series-group-s11-12')).toHaveAttribute('data-focused', 'true');
    keptRowSpy.mockClear();
    const user = userEvent.setup();
    await user.keyboard('j');
    expect(screen.getByTestId('series-group-s21-22')).toHaveAttribute('data-focused', 'true');
    expect(screen.getByTestId('series-group-s11-12')).not.toHaveAttribute('data-focused');
    expect(keptRowSpy).toHaveBeenCalledTimes(2);
  });
});
