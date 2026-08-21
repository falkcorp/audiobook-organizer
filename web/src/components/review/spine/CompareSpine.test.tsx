// file: web/src/components/review/spine/CompareSpine.test.tsx
// version: 1.2.0
// guid: f30a6c85-2b47-4e19-93d0-8a5c1e7b402f
// last-edited: 2026-08-21

import { describe, expect, it, vi, beforeEach } from 'vitest';
import { render, screen, within } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { ThemeProvider } from '@mui/material/styles';
import { CompareSpine, SPINE_TWO_COLUMN_MIN, type SpineContext } from './CompareSpine';
import type { CandidateGroup } from './CompareSpine';
import { appTheme } from '../../../theme';
import * as api from '../../../services/api';
import type { CandidateResult, Config, MetadataCandidate, PathAlias } from '../../../services/api';
import type { MetadataAction } from '../reviewActions';
import type { RowState } from './rowState';

// Task 7 wired PathLinks into all three CompareSpine render sites
// (GroupedCard, CompactRow, TwoColumnCard), which pulls in usePathVars()
// (formatPath.ts) and usePathAliases() (PathLinks.tsx) -- both call
// api.getConfig() independently, and CompareSpine now calls usePathAliases()
// itself rather than taking aliases as an explicit prop. Automock the module
// and supply path_aliases so the derived Windows/UNC rows have something to
// match; without this the real apiFetch call resolves via the generic {}
// fallback in src/test/setup.ts, which yields path_aliases: [] and a
// POSIX-only render (harmless for tests that don't care about path display,
// but wrong for the ones below that do).
vi.mock('../../../services/api');

const ALIASES: PathAlias[] = [
  { root: '/library/books', windows: 'W:', unc: '\\\\host\\books', smb_url: 'smb://host/books' },
];

beforeEach(() => {
  vi.mocked(api.getConfig).mockResolvedValue({ root_dir: '', path_aliases: ALIASES } as Config);
});

// These renderers were ported from MetadataReviewDialog by mechanical
// substitution, not rewritten. The tests therefore focus on the seams the
// substitution created -- dispatch instead of direct calls, accessors instead of
// closure reads -- plus the details docs/port-inventory.md says must survive.

const candidate = {
  title: 'Mistborn: The Final Empire',
  author: 'Brandon Sanderson',
  narrator: 'Michael Kramer',
  source: 'audible',
  score: 0.92,
  cover_url: 'https://example.invalid/c.jpg',
  duration_delta_sec: 120,
} as unknown as MetadataCandidate;

function row(id: string, over: Partial<CandidateResult> = {}): CandidateResult {
  return {
    book: {
      id,
      title: `Book ${id}`,
      author: 'Someone',
      cover_url: '',
      file_path: `/audio/${id}.m4b`,
      duration_seconds: 43200,
      file_size_bytes: 350 * 1048576,
      format: 'm4b',
    },
    candidate,
    status: 'matched',
    ...over,
  } as unknown as CandidateResult;
}

function makeCtx(over: Partial<SpineContext> = {}) {
  const onAction = vi.fn<(a: MetadataAction) => void>();
  const states = new Map<string, RowState>();
  const ctx: SpineContext = {
    rowState: (id) => states.get(id),
    isSelected: () => false,
    onToggleSelect: vi.fn(),
    onPreviewCover: vi.fn(),
    onAction,
    expandedId: null,
    onToggleExpand: vi.fn(),
    ...over,
  };
  return { ctx, onAction, states };
}

function renderSpine(ui: Parameters<typeof CompareSpine>[0]) {
  return render(
    <ThemeProvider theme={appTheme} defaultMode="dark">
      <CompareSpine {...ui} />
    </ThemeProvider>
  );
}

describe('view mode dispatch', () => {
  it('renders the mode it is asked for and records it on the container', () => {
    const { ctx } = makeCtx();
    const { rerender } = renderSpine({ rows: [row('b1')], viewMode: 'compact', ctx });
    expect(screen.getByTestId('compare-spine')).toHaveAttribute('data-view-mode', 'compact');
    expect(screen.queryByTestId('spine-auto-card')).not.toBeInTheDocument();

    rerender(
      <ThemeProvider theme={appTheme} defaultMode="dark">
        <CompareSpine rows={[row('b1')]} viewMode="auto" ctx={ctx} />
      </ThemeProvider>
    );
    expect(screen.getByTestId('spine-auto-card')).toBeInTheDocument();
  });

  it('explains an empty spine rather than rendering a blank box', () => {
    const { ctx } = makeCtx();
    renderSpine({ rows: [], viewMode: 'compact', ctx, emptyMessage: 'Nothing left to review.' });
    expect(screen.getByText('Nothing left to review.')).toBeInTheDocument();
  });
});

describe('actions are dispatched, not performed', () => {
  it('emits apply/reject/skip from the compact row', async () => {
    const user = userEvent.setup();
    const { ctx, onAction } = makeCtx();
    renderSpine({ rows: [row('b1')], viewMode: 'compact', ctx });

    await user.click(screen.getByRole('button', { name: 'Apply' }));
    expect(onAction).toHaveBeenCalledWith({ lane: 'metadata', type: 'apply', id: 'b1' });

    await user.click(screen.getByRole('button', { name: 'Reject' }));
    expect(onAction).toHaveBeenCalledWith({ lane: 'metadata', type: 'reject', id: 'b1' });

    await user.click(screen.getByRole('button', { name: 'Skip' }));
    expect(onAction).toHaveBeenCalledWith({ lane: 'metadata', type: 'skip', id: 'b1' });
  });

  it('distinguishes skip from unskip, which the dialog could not', () => {
    // The dialog's single `handleSkip` toggled 'skipped' <-> 'pending' (:592),
    // so the Skip button and the "Skipped" chip called the same function and
    // meant opposite things. Splitting them is the port's one semantic change,
    // and this is the assertion that it happened.
    const { ctx, onAction } = makeCtx({ rowState: () => 'skipped' });
    renderSpine({ rows: [row('b1')], viewMode: 'compact', ctx });

    const chip = screen.getByText('Skipped');
    chip.click();
    expect(onAction).toHaveBeenCalledWith({ lane: 'metadata', type: 'unskip', id: 'b1' });
    expect(onAction).not.toHaveBeenCalledWith({ lane: 'metadata', type: 'skip', id: 'b1' });
  });

  it('offers undo on a rejected row', async () => {
    const user = userEvent.setup();
    const { ctx, onAction } = makeCtx({ rowState: () => 'rejected' });
    renderSpine({ rows: [row('b1')], viewMode: 'compact', ctx });

    await user.click(screen.getByText('Rejected — click to undo'));
    expect(onAction).toHaveBeenCalledWith({ lane: 'metadata', type: 'unreject', id: 'b1' });
  });
});

describe('row state governs what can be done', () => {
  it('hides the action buttons on a closed row', () => {
    // `isRowActionable` says applied and rejected are closed. The buttons must
    // actually disappear, not merely be ignored on click.
    const { ctx } = makeCtx({ rowState: () => 'applied' });
    renderSpine({ rows: [row('b1')], viewMode: 'compact', ctx });
    expect(screen.queryByRole('button', { name: 'Apply' })).not.toBeInTheDocument();
    expect(screen.getByText('Applied')).toBeInTheDocument();
  });

  it('keeps a skipped row actionable', () => {
    // The asymmetry from rowState.ts, verified where it actually matters: a
    // reviewer who skipped something must be able to come back and apply it.
    const { ctx } = makeCtx({ rowState: () => 'skipped' });
    renderSpine({ rows: [row('b1')], viewMode: 'compact', ctx });
    expect(screen.getByRole('button', { name: 'Apply' })).toBeInTheDocument();
  });

  it('disables selection on a closed row', () => {
    const { ctx } = makeCtx({ rowState: () => 'rejected' });
    renderSpine({ rows: [row('b1')], viewMode: 'compact', ctx });
    expect(screen.getByRole('checkbox')).toBeDisabled();
  });
});

describe('what the reviewer must be able to see', () => {
  it('shows the match, its score, and its provider', () => {
    const { ctx } = makeCtx();
    renderSpine({ rows: [row('b1')], viewMode: 'compact', ctx });
    expect(screen.getByText('Mistborn: The Final Empire')).toBeInTheDocument();
    expect(screen.getByText('92%')).toBeInTheDocument();
    expect(screen.getByText('audible')).toBeInTheDocument();
  });

  it('warns when the runtimes disagree by more than ten minutes', () => {
    // Carried from the dialog. A large runtime gap usually means an abridgement
    // or the wrong book, and it is the cheapest signal that an otherwise
    // high-scoring match is wrong.
    const { ctx } = makeCtx();
    // A fresh candidate, NOT a mutation of the shared one: `row()` hands every
    // fixture the same object reference, so mutating it here would leak a
    // one-hour runtime gap into every other test in this file and make the
    // suite order-dependent.
    const mismatched = row('b1', {
      candidate: { ...candidate, duration_delta_sec: -3600 } as MetadataCandidate,
    });
    renderSpine({ rows: [mismatched], viewMode: 'compact', ctx });
    expect(screen.getByText(/runtime differs by 1h 0m/)).toBeInTheDocument();
  });

  it('marks a book with no match, distinctly from one with an error', () => {
    const { ctx } = makeCtx();
    renderSpine({
      rows: [
        row('b1', { candidate: undefined, status: 'no_match' }),
        row('b2', { candidate: undefined, status: 'error' }),
      ],
      viewMode: 'compact',
      ctx,
    });
    expect(screen.getByText('No match')).toBeInTheDocument();
    expect(screen.getByText('Error')).toBeInTheDocument();
  });
});

describe('grouped results', () => {
  const group: CandidateGroup = {
    key: 'g1',
    candidate,
    results: [row('b1'), row('b2'), row('b3')],
  };

  it('renders a group as one card naming how many files share the match', () => {
    // A group is several books competing for ONE candidate. Rendering it as a
    // row per book would repeat the candidate and imply each book had its own.
    const { ctx } = makeCtx();
    renderSpine({ rows: [], groups: [group], viewMode: 'compact', ctx });
    expect(screen.getByText('3 files matched to the same book')).toBeInTheDocument();
  });

  it('groups render regardless of view mode', () => {
    const { ctx } = makeCtx();
    renderSpine({ rows: [], groups: [group], viewMode: 'two-column', ctx });
    expect(screen.getByText('3 files matched to the same book')).toBeInTheDocument();
  });

  it('rejects the whole group in one action, not one per book', async () => {
    const user = userEvent.setup();
    const { ctx, onAction } = makeCtx();
    renderSpine({ rows: [], groups: [group], viewMode: 'compact', ctx });

    await user.click(screen.getByRole('button', { name: /reject all/i }));
    expect(onAction).toHaveBeenCalledWith({
      lane: 'metadata',
      type: 'rejectGroup',
      ids: ['b1', 'b2', 'b3'],
    });
  });

  it('lets a book be separated from its group', async () => {
    const user = userEvent.setup();
    const { ctx, onAction } = makeCtx();
    renderSpine({ rows: [], groups: [group], viewMode: 'compact', ctx });

    const [first] = screen.getAllByRole('button', { name: /separate from group/i });
    await user.click(first);
    expect(onAction).toHaveBeenCalledWith({ lane: 'metadata', type: 'ungroup', id: 'b1' });
  });

  it('excludes already-decided books from the group bulk actions', () => {
    // `actionableIds` filters by isRowActionable. A group where one book was
    // already applied must not re-apply it.
    const decided = new Map<string, RowState>([['b2', 'applied']]);
    const { ctx, onAction } = makeCtx({ rowState: (id) => decided.get(id) });
    renderSpine({ rows: [], groups: [group], viewMode: 'compact', ctx });

    screen.getByRole('button', { name: /reject all/i }).click();
    expect(onAction).toHaveBeenCalledWith({
      lane: 'metadata',
      type: 'rejectGroup',
      ids: ['b1', 'b3'],
    });
  });
});

describe('expansion is compact-only', () => {
  it('expands the row the context names', () => {
    const { ctx } = makeCtx({ expandedId: 'b1' });
    renderSpine({ rows: [row('b1')], viewMode: 'compact', ctx });
    // The expanded detail adds a "Current" / "Proposed" comparison that the
    // collapsed row does not have.
    expect(screen.getByText('Current')).toBeInTheDocument();
  });

  it('asks the owner to toggle rather than holding the state itself', async () => {
    const user = userEvent.setup();
    const onToggleExpand = vi.fn();
    const { ctx } = makeCtx({ onToggleExpand });
    renderSpine({ rows: [row('b1')], viewMode: 'compact', ctx });

    // The title cell holds "Book b1 -> <strong>Mistborn...</strong>", so the
    // book title is split across elements; match on the substring instead.
    await user.click(screen.getByText(/Book b1/));
    expect(onToggleExpand).toHaveBeenCalledWith('b1');
  });
});

describe('auto mode', () => {
  it('makes the spine the container the query resolves against', () => {
    // If `container-type` lands on the row instead of the spine, the query
    // measures a box that is already as wide as the spine and the collapse never
    // fires. jsdom does not evaluate container queries, so this asserts the
    // declaration is on the right element; whether it reflows is a browser
    // question for the visual harness.
    const { ctx } = makeCtx();
    renderSpine({ rows: [row('b1')], viewMode: 'auto', ctx });
    const spine = screen.getByTestId('compare-spine');
    expect(getComputedStyle(spine).containerType).toBe('inline-size');
  });

  it('collapses below the spine width, not the window width', () => {
    // The bug being fixed: the dialog's two-column card has no responsive
    // collapse at all, so beside a queue rail on a laptop both columns squish.
    // A media query cannot fix it -- the window is wide while the spine is not.
    expect(SPINE_TWO_COLUMN_MIN).toBe(700);
    const { ctx } = makeCtx();
    renderSpine({ rows: [row('b1')], viewMode: 'auto', ctx });
    expect(screen.getByTestId('spine-auto-card')).toBeInTheDocument();
  });

  it('reuses the two-column renderer rather than forking it', () => {
    // Forking would double what Phase 7's inventory has to check, and the two
    // copies would drift. The auto card must contain the same content.
    const { ctx } = makeCtx();
    renderSpine({ rows: [row('b1')], viewMode: 'auto', ctx });
    const auto = screen.getByTestId('spine-auto-card');
    expect(within(auto).getByText('Mistborn: The Final Empire')).toBeInTheDocument();
  });
});

describe('dual path display (Task 7)', () => {
  it('renders the stored iTunes path and the derived windows path side by side', async () => {
    // A row whose stored iTunes path disagrees with what file_path derives to.
    // Both must show: the stored line is provenance (where iTunes recorded the
    // file), the derived line is a transform of current belief (what the app
    // thinks file_path is now), and the disagreement is a corruption signal a
    // reviewer needs to see -- not redundancy to be collapsed away.
    //
    // The compact row's file-path lines live inside the expanded "Current"
    // detail (CompareSpine.tsx :546), not the collapsed row -- so this must
    // expand b1 to reach them.
    const { ctx } = makeCtx({ expandedId: 'b1' });
    renderSpine({
      rows: [
        row('b1', {
          book: {
            id: 'b1',
            title: 'Book b1',
            author: 'Someone',
            cover_url: '',
            file_path: '/library/books/Author/Title/x.m4b',
            itunes_path: 'W:\\itunes\\Old Location\\x.m4b',
            duration_seconds: 43200,
            file_size_bytes: 350 * 1048576,
            format: 'm4b',
          },
        }),
      ],
      viewMode: 'compact',
      ctx,
    });

    // The stored line: still blue, still "iTunes:"-prefixed -- untouched by
    // Task 7.
    expect(await screen.findByText(/iTunes: W:\\itunes\\Old Location/)).toBeInTheDocument();
    // The derived line: plain, unprefixed, a transform of file_path via the
    // configured alias. Neither line is the other.
    expect(screen.getByText(/^W:\\Author\\Title\\x\.m4b$/)).toBeInTheDocument();
  });

  it('threads pathAliases through so the Windows and UNC rows actually render', async () => {
    // The POSIX row renders regardless of whether aliases ever arrive (see
    // PathLinks.test.tsx), so asserting only the POSIX row would pass even if
    // CompareSpine never wired usePathAliases() through to a render site at
    // all. Only the Windows/UNC rows are proof the alias actually matched.
    // Exercises TwoColumnCard (site 3) -- the coexistence test above already
    // covers CompactRow (site 2).
    const { ctx } = makeCtx();
    renderSpine({
      rows: [
        row('b1', {
          book: {
            id: 'b1',
            title: 'Book b1',
            author: 'Someone',
            cover_url: '',
            file_path: '/library/books/Author/Title/x.m4b',
            duration_seconds: 43200,
            file_size_bytes: 350 * 1048576,
            format: 'm4b',
          },
        }),
      ],
      viewMode: 'two-column',
      ctx,
    });

    expect(await screen.findByRole('button', { name: 'Copy Windows path' })).toBeInTheDocument();
    expect(screen.getByRole('button', { name: 'Copy UNC path' })).toBeInTheDocument();
  });

  it('threads pathAliases through GroupedCard so the Windows and UNC rows render', async () => {
    // GroupedCard (site 1) is reached only via `groups`, never `rows` -- the
    // 'grouped results' describe block above renders groups too, but never
    // asserts on path output, so a regression that dropped
    // pathAliases={pathAliases} from just the GroupedCard call site
    // (CompareSpine.tsx :1071) would pass every other test in this file. The
    // two-column test above covers site 3 and the coexistence test covers
    // site 2 (CompactRow); neither renders a group.
    const { ctx } = makeCtx();
    const group: CandidateGroup = {
      key: 'g1',
      candidate,
      results: [
        row('b1', {
          book: {
            id: 'b1',
            title: 'Book b1',
            author: 'Someone',
            cover_url: '',
            file_path: '/library/books/Author/Title/y.m4b',
            duration_seconds: 43200,
            file_size_bytes: 350 * 1048576,
            format: 'm4b',
          },
        }),
      ],
    };
    renderSpine({ rows: [], groups: [group], viewMode: 'compact', ctx });

    expect(await screen.findByRole('button', { name: 'Copy Windows path' })).toBeInTheDocument();
    expect(screen.getByRole('button', { name: 'Copy UNC path' })).toBeInTheDocument();
  });
});
