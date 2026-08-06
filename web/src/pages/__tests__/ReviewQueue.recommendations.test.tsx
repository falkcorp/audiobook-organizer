// file: web/src/pages/__tests__/ReviewQueue.recommendations.test.tsx
// version: 1.0.0
// guid: 0d7a3e51-6c92-4b48-a05f-8e1c2d6b9f34
// last-edited: 2026-08-06

// Owner items 1+2 on the client: the queue shows WHY each hold exists (reason +
// evidence numbers), and lets a human override the machine per item.
//
// The assertions that matter are the refusals — an insufficient-evidence hold must
// not offer a working Approve, and an override must be what actually goes on the
// wire. Both are places where a plausible-looking UI would quietly do the wrong
// thing: pre-filling `combine` on a hold with no evidence, or showing "separate" and
// posting nothing.

import { describe, it, expect, vi, beforeEach } from 'vitest';
import { screen, fireEvent, waitFor, within } from '@testing-library/react';
import { renderWithProviders } from '../../test/renderWithProviders';
import { ReviewQueue } from '../ReviewQueue';
import { useReviewStore } from '../../stores/useReviewStore';
import * as api from '../../services/api';

function mkItem(id: string, payload: Record<string, unknown>): api.ReviewItem {
  return {
    id,
    kind: 'regroup.multidisc',
    dedup_key: `dk-${id}`,
    folder_ref: `/books/${id}`,
    status: 'pending',
    summary: `hold ${id}`,
    payload: JSON.stringify(payload),
    created_at: '2026-08-06T00:00:00Z',
    updated_at: '2026-08-06T00:00:00Z',
  };
}

// A decisive hold: the classifier says separate and shows its arithmetic.
const decisive = mkItem('itm-decisive', {
  folder: '/books/series',
  recommendedAction: 'separate',
  recommendationReason:
    'a strict majority of members (4 of 5 known runtimes) are book-length; the longest runs 15.8 h',
  recommendationEvidence: {
    members: 5,
    durationsKnown: 5,
    bookLengthMembers: 4,
    medianKnownSec: 40000,
    longestKnownSec: 56880,
    distinctStems: 5,
    numberedMembers: 5,
    structure: 'flat',
  },
});

// The shape of every hold currently in prod's queue: no recommendation at all.
const undecidable = mkItem('itm-old', { folder: '/books/old' });

function seedQueue(items: api.ReviewItem[]) {
  useReviewStore.setState({
    count: items.length,
    byKind: { 'regroup.multidisc': items.length },
    items,
    itemsLoading: false,
    _pollTimer: null,
    loadItems: vi.fn().mockResolvedValue(undefined),
    loadCount: vi.fn().mockResolvedValue(undefined),
  });
}

/** openHold expands a hold's accordion and returns its details region. */
function openHold(summary: string) {
  fireEvent.click(screen.getByText(summary));
  return screen.getByText(summary).closest('.MuiAccordion-root') as HTMLElement;
}

describe('ReviewQueue recommendations', () => {
  beforeEach(() => {
    vi.restoreAllMocks();
  });

  it('shows the per-hold reason instead of one generic string', () => {
    seedQueue([decisive]);
    renderWithProviders(<ReviewQueue />);
    openHold('hold itm-decisive');
    expect(screen.getByText(/the longest runs 15.8 h/)).toBeInTheDocument();
    expect(screen.getByText(/Recommended: Keep separate/)).toBeInTheDocument();
  });

  it('shows the evidence numbers, not just the reason', () => {
    // A reason without its arithmetic is just a nicer generic string.
    seedQueue([decisive]);
    renderWithProviders(<ReviewQueue />);
    const hold = openHold('hold itm-decisive');
    expect(within(hold).getByText('5 members')).toBeInTheDocument();
    expect(within(hold).getByText('5/5 runtimes known')).toBeInTheDocument();
    expect(within(hold).getByText('4 book-length')).toBeInTheDocument();
    expect(within(hold).getByText('median 11.1 h')).toBeInTheDocument();
    expect(within(hold).getByText('longest 15.8 h')).toBeInTheDocument();
    expect(within(hold).getByText('5 distinct titles')).toBeInTheDocument();
  });

  it('flags an undecidable hold in the collapsed row so a bucket can be triaged', () => {
    seedQueue([undecidable]);
    renderWithProviders(<ReviewQueue />);
    expect(screen.getByText('Needs a decision')).toBeInTheDocument();
  });

  // 🔴 The backend 400s `insufficient-evidence` on purpose. Offering an Approve that
  // always fails — or, worse, pre-filling `combine` on the hold with the least
  // evidence — is the failure this asserts against.
  it('disables Approve on an insufficient-evidence hold until an action is picked', async () => {
    const approve = vi.spyOn(api, 'approveReviewItem').mockResolvedValue(undecidable);
    seedQueue([undecidable]);
    renderWithProviders(<ReviewQueue />);
    const hold = openHold('hold itm-old');

    const approveButtons = within(hold).getAllByRole('button', { name: /approve/i });
    approveButtons.forEach((b) => expect(b).toBeDisabled());
    expect(approve).not.toHaveBeenCalled();

    // Choosing an action unlocks it, and that action is what is sent.
    fireEvent.mouseDown(within(hold).getByLabelText('Action'));
    fireEvent.click(await screen.findByRole('option', { name: /combine/i }));
    await waitFor(() =>
      expect(within(hold).getAllByRole('button', { name: /approve/i })[0]).toBeEnabled()
    );
    fireEvent.click(within(hold).getAllByRole('button', { name: /approve/i })[0]);
    await waitFor(() => expect(approve).toHaveBeenCalledWith('itm-old', 'combine'));
  });

  it('sends the recommendation when the reviewer does not override it', async () => {
    const approve = vi.spyOn(api, 'approveReviewItem').mockResolvedValue(decisive);
    seedQueue([decisive]);
    renderWithProviders(<ReviewQueue />);
    const hold = openHold('hold itm-decisive');
    fireEvent.click(within(hold).getAllByRole('button', { name: /approve/i })[0]);
    await waitFor(() => expect(approve).toHaveBeenCalledWith('itm-decisive', 'separate'));
  });

  // 🔴 OWNER ITEM 2 ON THE CLIENT. What the selector shows must be what the request
  // carries; a UI that displayed the override and posted the recommendation would be
  // worse than not offering the override at all.
  it('sends the OVERRIDE, not the recommendation, when the reviewer changes it', async () => {
    const approve = vi.spyOn(api, 'approveReviewItem').mockResolvedValue(decisive);
    seedQueue([decisive]);
    renderWithProviders(<ReviewQueue />);
    const hold = openHold('hold itm-decisive');

    fireEvent.mouseDown(within(hold).getByLabelText('Action'));
    fireEvent.click(await screen.findByRole('option', { name: /^Combine$/ }));
    fireEvent.click(within(hold).getAllByRole('button', { name: /approve/i })[0]);
    await waitFor(() => expect(approve).toHaveBeenCalledWith('itm-decisive', 'combine'));
  });

  it('never offers insufficient-evidence as a choice', async () => {
    seedQueue([decisive]);
    renderWithProviders(<ReviewQueue />);
    const hold = openHold('hold itm-decisive');
    fireEvent.mouseDown(within(hold).getByLabelText('Action'));
    const options = (await screen.findAllByRole('option')).map((o) => o.textContent);
    expect(options).not.toContain('Not enough evidence');
    // duplicate-of IS offered — the backend 501s it, and hiding it would misrepresent
    // the vocabulary while faking success would mark a hold decided doing nothing.
    expect(options.some((o) => o?.includes('Duplicate of an existing book'))).toBe(true);
  });

  // 🔴 A bulk action that silently skips items is how a reviewer thinks they cleared
  // a queue they did not.
  it('surfaces the ids a bulk approve skipped', async () => {
    vi.spyOn(api, 'bulkReviewAction').mockResolvedValue({
      action: 'approve',
      processed: 1,
      approved: ['itm-decisive'],
      skipped: [
        {
          id: 'itm-old',
          action: 'insufficient-evidence',
          reason: 'this hold recommends insufficient-evidence',
        },
      ],
    });
    seedQueue([decisive, undecidable]);
    renderWithProviders(<ReviewQueue />);

    fireEvent.click(screen.getByRole('button', { name: /approve all/i }));
    expect(await screen.findByText(/1 hold was skipped — still pending/)).toBeInTheDocument();
    expect(screen.getByText('itm-old')).toBeInTheDocument();
  });
});
