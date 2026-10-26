// file: web/src/components/review/spine/RegroupSpine.test.tsx
// version: 1.7.0
// guid: 3f7c9e21-5a4b-4d8e-9c1f-2b6a7d0e4c53
// last-edited: 2026-10-25

/**
 * Task 6 fix round: RegroupSpine wired PathLinks into MemberRow (commit
 * a21ebdb6) with no covering test, so the wiring could silently render on one
 * lane and not another. This file exercises MemberRow directly rather than
 * the whole RegroupSpine tree -- the only thing Task 6 touched is whether
 * `entry.filePath` / `pathAliases` reach PathLinks and whether the guard
 * holds, and PathLinks.test.tsx already covers PathLinks's own
 * rendering/aliasing logic in depth.
 */

import { describe, it, expect, vi, beforeEach } from 'vitest';
import { render, screen, waitFor } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { ThemeProvider } from '@mui/material/styles';
import { ApplyingAlert, MemberRow, NeedsAttentionAlert, SkipsAlert } from './RegroupSpine';
import type { RegroupLane } from '../lanes/useRegroupLane';
import { appTheme } from '../../../theme';
import * as api from '../../../services/api';
import type { PathAlias, Config } from '../../../services/api';
import type { MemberEntry } from '../../../lib/reviewPayload';

// MemberRow receives `pathAliases` as a plain prop -- it does not call
// usePathAliases() itself (only MemberFilesDetail does, one level up). So the
// only reason PathLinks needs the api module mocked at all is that it calls
// usePathVars() (formatPath.ts) internally for display abbreviation; without
// this, getConfig()'s real apiFetch call resolves via the generic {}
// fallback in src/test/setup.ts, which is harmless but noisy (a thrown-then-
// caught TypeError inside the vars promise chain on every render).
vi.mock('../../../services/api');

beforeEach(() => {
  vi.mocked(api.getConfig).mockResolvedValue({ root_dir: '' } as Config);
});

const ALIASES: PathAlias[] = [
  { root: '/library/books', windows: 'W:', unc: '\\\\host\\books', smb_url: 'smb://host/books' },
];

function entry(filePath: string): MemberEntry {
  return { filePath };
}

function renderRow(e: MemberEntry, aliases: PathAlias[] = []) {
  return render(
    <ThemeProvider theme={appTheme} defaultMode="dark">
      <MemberRow entry={e} book={undefined} pathAliases={aliases} pathVars={[]} />
    </ThemeProvider>
  );
}

describe('MemberRow / PathLinks wiring', () => {
  it('renders a monospace path row with its copy button when entry.filePath is set', () => {
    renderRow(entry('/library/books/a.m4b'));
    const copyButton = screen.getByRole('button', { name: 'Copy Linux path' });
    expect(copyButton).toBeInTheDocument();
    const pathText = screen.getByText('/library/books/a.m4b');
    expect(getComputedStyle(pathText).fontFamily).toBe('monospace');
  });

  it('threads pathAliases through so the Windows and UNC rows actually render', () => {
    // The POSIX row renders even with an empty aliases array (see the test
    // above), so a test that only checked for the POSIX row would still pass
    // if `pathAliases` never made it from MemberRow's prop to PathLinks --
    // that is exactly the regression this test exists to catch. Only the
    // Windows/UNC rows are proof the alias actually matched and was used.
    renderRow(entry('/library/books/Author/Title/x.m4b'), ALIASES);
    expect(screen.getByRole('button', { name: 'Copy Windows path' })).toBeInTheDocument();
    expect(screen.getByRole('button', { name: 'Copy UNC path' })).toBeInTheDocument();
  });

  it('renders no path row, and does not crash, when entry.filePath is falsy', () => {
    renderRow(entry(''), ALIASES);
    expect(screen.queryByRole('button', { name: /copy/i })).not.toBeInTheDocument();
  });

  it('does not throw clicking copy without a real clipboard or a mounted ToastProvider', async () => {
    // No ToastProvider wraps this render -- useToast() falls back to the
    // no-op { toast: () => {} } from its default context (ToastProvider.tsx),
    // and jsdom does not implement navigator.clipboard, so this exercises
    // both fallbacks at once.
    const user = userEvent.setup();
    renderRow(entry('/library/books/a.m4b'));
    await user.click(screen.getByRole('button', { name: 'Copy Linux path' }));
    // Reaching here without an uncaught rejection/throw is the assertion.
    expect(screen.getByRole('button', { name: 'Copy Linux path' })).toBeInTheDocument();
  });
});

describe('SkipsAlert report panel', () => {
  const KIND = 'regroup.multidisc';

  function renderAlert(lane: Partial<RegroupLane>) {
    const full = {
      skipsByKind: {},
      failedByKind: {},
      unrecordedByKind: {},
      notesByKind: {},
      dismissSkips: vi.fn(),
      ...lane,
    } as unknown as RegroupLane;
    render(
      <ThemeProvider theme={appTheme} defaultMode="dark">
        <div data-testid="regroup-row-f1">row f1</div>
        <SkipsAlert kind={KIND} lane={full} />
      </ThemeProvider>
    );
    return full;
  }

  it('lists the notes of items that applied, as plain ids, outliving the toast', () => {
    renderAlert({
      notesByKind: {
        [KIND]: [
          { id: 'n1', note: 'combine of /lib/Gotham/Arkham/year-one.m4b: left a file unattached' },
        ],
      },
    });

    const notes = screen.getByTestId(`regroup-notes-${KIND}`);
    expect(notes).toHaveTextContent('1 applied with a note to read');
    expect(notes).toHaveTextContent('left a file unattached');
    // The item applied, so it left the pending list: no row to open.
    expect(screen.getByTestId('regroup-report-id-n1')).toBeInTheDocument();
    expect(screen.queryByTestId('regroup-report-open-n1')).toBeNull();
  });

  it('renders nothing when the kind has no skips, failures, unrecorded items or notes', () => {
    renderAlert({});
    expect(screen.queryByTestId(`regroup-skips-${KIND}`)).toBeNull();
  });

  it('lists failed items as clickable entries and unrecorded ones as plain ids, outliving the toast', async () => {
    const scroll = vi.fn();
    Element.prototype.scrollIntoView = scroll;
    const lane = renderAlert({
      failedByKind: { [KIND]: [{ id: 'f1', reason: 'synthetic refusal' }] },
      unrecordedByKind: { [KIND]: [{ id: 'u1', reason: 'applied but status write failed' }] },
    });

    expect(screen.getByTestId(`regroup-failed-${KIND}`)).toHaveTextContent('synthetic refusal');
    expect(screen.getByTestId(`regroup-unrecorded-${KIND}`)).toHaveTextContent(
      'Approving one again records it without running the apply again'
    );
    // An unrecorded item is held mid-apply and is not in the list: no row to open.
    expect(screen.getByTestId('regroup-report-id-u1')).toBeInTheDocument();
    expect(screen.queryByTestId('regroup-report-open-u1')).toBeNull();

    await userEvent.click(screen.getByTestId('regroup-report-open-f1'));
    expect(scroll).toHaveBeenCalledTimes(1);

    await userEvent.click(screen.getByRole('button', { name: /close/i }));
    expect(lane.dismissSkips).toHaveBeenCalledWith(KIND);
  });

  it('gives skips of applied or mid-apply items their own heading, not "still pending"', () => {
    renderAlert({
      skipsByKind: {
        [KIND]: [
          {
            id: 'p1',
            action: 'insufficient-evidence',
            reason: 'no decidable action',
            code: 'REVIEW_ACTION_NOT_DECIDABLE',
          },
          {
            id: 'd1',
            action: 'combine',
            reason: 'already applied',
            code: 'REVIEW_ALREADY_APPLIED',
          },
          {
            id: 'd2',
            action: 'combine',
            reason: 'an apply is in progress',
            code: 'REVIEW_APPLY_IN_PROGRESS',
          },
          {
            id: 'd3',
            action: 'combine',
            reason: 'needs attention and apply is off',
            code: 'REVIEW_APPLY_DISABLED',
          },
        ],
      },
    });
    const done = screen.getByTestId(`regroup-skipped-done-${KIND}`);
    expect(done).toHaveTextContent(
      '3 holds were skipped — no longer pending (applied, mid-apply or needs attention)'
    );
    expect(done).not.toHaveTextContent('still pending');
    expect(screen.getByTestId('regroup-report-id-d1')).toBeInTheDocument();
    const pending = screen.getByTestId(`regroup-skipped-${KIND}`);
    expect(pending).toHaveTextContent('1 hold was skipped — still pending');
    expect(pending).not.toHaveTextContent('d1');
  });
});

describe('SkipsAlert held-versus-pending skip codes', () => {
  const KIND = 'regroup.multidisc';

  // The code tells the report whether the item is still in a pending list. A re-run
  // refused by the iTunes guard leaves the item applying, and the server now answers
  // that as REVIEW_RERUN_FAILED; REVIEW_ITUNES_PROTECTED is a FIRST run that was
  // handed back, which is still pending and still in the list below the alert.
  it('files a refused re-run as held and a refused first run as still pending', () => {
    const lane = {
      skipsByKind: {
        [KIND]: [
          {
            id: 'r1',
            action: 'combine',
            reason: 'iTunes guard refused the re-run',
            code: 'REVIEW_RERUN_FAILED',
          },
          {
            id: 'i1',
            action: 'combine',
            reason: 'a member is under the active iTunes library',
            code: 'REVIEW_ITUNES_PROTECTED',
          },
        ],
      },
      failedByKind: {},
      unrecordedByKind: {},
      notesByKind: {},
      dismissSkips: vi.fn(),
    } as unknown as RegroupLane;
    render(
      <ThemeProvider theme={appTheme} defaultMode="dark">
        <div data-testid="regroup-row-i1">row i1</div>
        <SkipsAlert kind={KIND} lane={lane} />
      </ThemeProvider>
    );
    const done = screen.getByTestId(`regroup-skipped-done-${KIND}`);
    expect(done).toHaveTextContent(
      '1 hold was skipped — no longer pending (applied, mid-apply or needs attention)'
    );
    expect(done).toHaveTextContent('iTunes guard refused the re-run');
    expect(screen.getByTestId('regroup-report-id-r1')).toBeInTheDocument();
    const pending = screen.getByTestId(`regroup-skipped-${KIND}`);
    expect(pending).toHaveTextContent('1 hold was skipped — still pending');
    expect(pending).toHaveTextContent('a member is under the active iTunes library');
    expect(pending).not.toHaveTextContent('r1');
  });

  // A first run that failed after it may have written is HELD applying (it is in no
  // pending list), unlike a first run that failed having written nothing, which the
  // server hands back (REVIEW_ITUNES_PROTECTED is that case for the iTunes guard).
  it('files a held partial first run as no longer pending', () => {
    const lane = {
      skipsByKind: {
        [KIND]: [
          {
            id: 'p1',
            action: 'combine',
            reason: 'merge stopped after moving two of five files',
            code: 'REVIEW_APPLY_PARTIAL',
          },
          {
            id: 'i1',
            action: 'combine',
            reason: 'a member is under the active iTunes library',
            code: 'REVIEW_ITUNES_PROTECTED',
          },
        ],
      },
      failedByKind: {},
      unrecordedByKind: {},
      notesByKind: {},
      dismissSkips: vi.fn(),
    } as unknown as RegroupLane;
    render(
      <ThemeProvider theme={appTheme} defaultMode="dark">
        <div data-testid="regroup-row-i1">row i1</div>
        <SkipsAlert kind={KIND} lane={lane} />
      </ThemeProvider>
    );
    const done = screen.getByTestId(`regroup-skipped-done-${KIND}`);
    expect(done).toHaveTextContent(
      '1 hold was skipped — no longer pending (applied, mid-apply or needs attention)'
    );
    expect(done).toHaveTextContent('merge stopped after moving two of five files');
    expect(screen.getByTestId('regroup-report-id-p1')).toBeInTheDocument();
    const pending = screen.getByTestId(`regroup-skipped-${KIND}`);
    expect(pending).toHaveTextContent('1 hold was skipped — still pending');
    expect(pending).not.toHaveTextContent('p1');
  });
});

describe('ApplyingAlert', () => {
  it('renders nothing when no hold is held mid-apply', () => {
    render(<ApplyingAlert count={0} onChanged={vi.fn()} />);
    expect(screen.queryByTestId('regroup-applying')).toBeNull();
  });

  it('lists the held holds on demand and finishes one with no explicit action', async () => {
    vi.mocked(api.getReviewItems).mockResolvedValue(heldPage([heldItem('h1', 'applying')]));
    vi.mocked(api.approveReviewItem).mockResolvedValue({} as never);
    const onChanged = vi.fn();
    render(<ApplyingAlert count={1} onChanged={onChanged} />);

    expect(screen.getByTestId('regroup-applying')).toHaveTextContent('1 hold is held mid-apply');
    await userEvent.click(screen.getByTestId('regroup-applying-show'));
    expect(api.getReviewItems).toHaveBeenCalledWith({ status: 'applying', limit: 100 });
    await userEvent.click(await screen.findByTestId('regroup-applying-finish-h1'));
    // No action: the server resolves it to the claim's own action. The rendered
    // updated_at goes with it.
    expect(api.approveReviewItem).toHaveBeenCalledWith('h1', undefined, HELD_STAMP);
    await waitFor(() => expect(onChanged).toHaveBeenCalledTimes(1));
  });

  it('shows the note of a finished apply instead of dropping it', async () => {
    vi.mocked(api.getReviewItems).mockResolvedValue(heldPage([heldItem('h1', 'applying')]));
    vi.mocked(api.approveReviewItem).mockResolvedValue({
      item: heldItem('h1', 'applied'),
      note: 'combine of /lib/Gotham/Arkham/hush.m4b: left a file unattached',
    });
    render(<ApplyingAlert count={1} onChanged={vi.fn()} />);

    await userEvent.click(screen.getByTestId('regroup-applying-show'));
    await userEvent.click(await screen.findByTestId('regroup-applying-finish-h1'));

    expect(await screen.findByTestId('regroup-held-notice')).toHaveTextContent(
      'Approved, with a note: combine of /lib/Gotham/Arkham/hush.m4b: left a file unattached'
    );
  });

  it('abandons an interrupted hold and reloads the list', async () => {
    vi.mocked(api.getReviewItems).mockResolvedValue(heldPage([heldItem('h1', 'applying')]));
    vi.mocked(api.abandonReviewApply).mockResolvedValue({} as never);
    const onChanged = vi.fn();
    render(<ApplyingAlert count={1} onChanged={onChanged} />);

    await userEvent.click(screen.getByTestId('regroup-applying-show'));
    await userEvent.click(await screen.findByTestId('regroup-applying-abandon-h1'));
    expect(api.abandonReviewApply).toHaveBeenCalledWith('h1');
    await waitFor(() => expect(onChanged).toHaveBeenCalledTimes(1));
    await waitFor(() => expect(api.getReviewItems).toHaveBeenCalledTimes(2));
  });

  it('shows a refusal and reloads the list, since the row is stale', async () => {
    vi.mocked(api.getReviewItems).mockResolvedValue(heldPage([heldItem('h1', 'applying')]));
    vi.mocked(api.approveReviewItem).mockRejectedValue(
      new Error('the item changed since it was read')
    );
    const onChanged = vi.fn();
    render(<ApplyingAlert count={1} onChanged={onChanged} />);

    await userEvent.click(screen.getByTestId('regroup-applying-show'));
    await userEvent.click(await screen.findByTestId('regroup-applying-finish-h1'));
    expect(await screen.findByText('the item changed since it was read')).toBeInTheDocument();
    await waitFor(() => expect(api.getReviewItems).toHaveBeenCalledTimes(2));
    expect(onChanged).not.toHaveBeenCalled();
  });

  it('reloads the shown list when the polled count changes, and says when it is capped', async () => {
    vi.mocked(api.getReviewItems).mockResolvedValue(heldPage([heldItem('h1', 'applying')]));
    const { rerender } = render(<ApplyingAlert count={1} onChanged={vi.fn()} />);
    // Not loaded until asked for.
    rerender(<ApplyingAlert count={2} onChanged={vi.fn()} />);
    expect(api.getReviewItems).not.toHaveBeenCalled();

    await userEvent.click(screen.getByTestId('regroup-applying-show'));
    await waitFor(() => expect(api.getReviewItems).toHaveBeenCalledTimes(1));
    expect(await screen.findByText('Showing the first 1 of 2.')).toBeInTheDocument();
    rerender(<ApplyingAlert count={3} onChanged={vi.fn()} />);
    await waitFor(() => expect(api.getReviewItems).toHaveBeenCalledTimes(2));
  });
});

describe('NeedsAttentionAlert', () => {
  it('renders nothing when no hold needs attention', () => {
    render(<NeedsAttentionAlert count={0} onChanged={vi.fn()} />);
    expect(screen.queryByTestId('regroup-needs-attention')).toBeNull();
  });

  it('lists the abandoned holds and approves one as its abandoned action', async () => {
    vi.mocked(api.getReviewItems).mockResolvedValue(
      heldPage([{ ...heldItem('n1', 'needs_attention'), apply_abandoned_action: 'combine' }])
    );
    vi.mocked(api.approveReviewItem).mockResolvedValue({} as never);
    const onChanged = vi.fn();
    render(<NeedsAttentionAlert count={1} onChanged={onChanged} />);

    expect(screen.getByTestId('regroup-needs-attention')).toHaveTextContent(
      '1 hold needs attention'
    );
    await userEvent.click(screen.getByTestId('regroup-needs-attention-show'));
    expect(api.getReviewItems).toHaveBeenCalledWith({ status: 'needs_attention', limit: 100 });
    await userEvent.click(await screen.findByTestId('regroup-needs-attention-approve-n1'));
    expect(api.approveReviewItem).toHaveBeenCalledWith('n1', 'combine', HELD_STAMP);
    await waitFor(() => expect(onChanged).toHaveBeenCalledTimes(1));
  });

  it('shows the refusal when approving is refused because apply is off, and leaves the hold listed', async () => {
    const message =
      'this item needs attention because an apply of "combine" was abandoned part way, so approving it ' +
      'means running that apply again, which needs review_apply_enabled to be on. ' +
      'Enable it and approve again, or reject the item';
    vi.mocked(api.getReviewItems).mockResolvedValue(
      heldPage([{ ...heldItem('n1', 'needs_attention'), apply_abandoned_action: 'combine' }])
    );
    vi.mocked(api.approveReviewItem).mockRejectedValue(
      Object.assign(new Error(message), { status: 409, data: { code: 'REVIEW_APPLY_DISABLED' } })
    );
    const onChanged = vi.fn();
    render(<NeedsAttentionAlert count={1} onChanged={onChanged} />);

    await userEvent.click(screen.getByTestId('regroup-needs-attention-show'));
    await userEvent.click(await screen.findByTestId('regroup-needs-attention-approve-n1'));
    expect(await screen.findByText(message)).toBeInTheDocument();
    // The list reloads (the row may be stale) and the hold is still there to reject.
    await waitFor(() => expect(api.getReviewItems).toHaveBeenCalledTimes(2));
    expect(screen.getByTestId('regroup-needs-attention-reject-n1')).toBeInTheDocument();
    expect(onChanged).not.toHaveBeenCalled();
  });

  it('rejects one with the rendered updated_at', async () => {
    vi.mocked(api.getReviewItems).mockResolvedValue(
      heldPage([{ ...heldItem('n1', 'needs_attention'), apply_abandoned_action: 'combine' }])
    );
    vi.mocked(api.rejectReviewItem).mockResolvedValue({} as never);
    render(<NeedsAttentionAlert count={1} onChanged={vi.fn()} />);

    await userEvent.click(screen.getByTestId('regroup-needs-attention-show'));
    await userEvent.click(await screen.findByTestId('regroup-needs-attention-reject-n1'));
    expect(api.rejectReviewItem).toHaveBeenCalledWith('n1', HELD_STAMP);
  });
});

const HELD_STAMP = '2026-10-18T07:00:00.123456789Z';

function heldItem(id: string, status: string): api.ReviewItem {
  return {
    id,
    kind: 'regroup.multidisc',
    dedup_key: 'k-' + id,
    folder_ref: '/f',
    status,
    summary: 'Chapters: 3 tracks',
    payload: '{}',
    created_at: '',
    updated_at: HELD_STAMP,
    apply_claim_action: status === 'applying' ? 'combine' : undefined,
  };
}

function heldPage(items: api.ReviewItem[]): api.ReviewItemsPage {
  return { items, count: items.length, total: items.length, limit: 100, offset: 0 };
}
