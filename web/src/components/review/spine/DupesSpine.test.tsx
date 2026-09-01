// file: web/src/components/review/spine/DupesSpine.test.tsx
// version: 2.2.0
// guid: 2b6d9e40-51c8-4a37-8f92-c704a1d5e836
// last-edited: 2026-09-01
//
// Covers the signal chips on a dupes row, plus (Task 8) the dual-path display
// wired into BookSide.
//
// The chip coverage was written because a mutation exposed the gap: deleting
// the entire chip-render block left all 687 other tests green. `primarySignals`
// was well covered as a function, which proves nothing about whether a chip
// reaches the screen -- and reaching the screen is the whole feature, since its
// purpose is to answer "why is this pair here" WITHOUT the reviewer expanding
// anything.
//
// Task 8 wired PathLinks into BookSide, which pulls in usePathVars()
// (formatPath.ts) and usePathAliases() (PathLinks.tsx) -- both call
// api.getConfig() independently, and DupesSpine now calls usePathAliases()
// itself once and threads it down as a prop. Automock the module and supply
// path_aliases so the derived Windows/UNC rows have something to match;
// without this the real apiFetch call resolves via the generic {} fallback in
// src/test/setup.ts, which yields path_aliases: [] and a POSIX-only render
// (harmless for the chip tests above, which don't set file_path, but wrong for
// the wiring tests below that do).
import { describe, expect, it, vi, beforeEach } from 'vitest';
import { render, screen } from '@testing-library/react';
import { ThemeProvider } from '@mui/material/styles';
import { MemoryRouter } from 'react-router-dom';
import { DupesSpine, type DupesSpineContext } from './DupesSpine';
import { appTheme } from '../../../theme';
import * as api from '../../../services/api';
import type { Config, DedupCandidate, PathAlias } from '../../../services/api';
import { __resetPathVarsCacheForTests } from '../../../utils/formatPath';
import { __resetPathAliasesCacheForTests } from '../../common/PathLinks';

vi.mock('../../../services/api');

const ALIASES: PathAlias[] = [
  { root: '/library/books', windows: 'W:', unc: '\\\\host\\books', smb_url: 'smb://host/books' },
];

beforeEach(() => {
  // Both usePathAliases and usePathVars memoize their config fetch at module
  // scope, so without these resets the first test in this file seeds the
  // answer for every later one -- and the pathVars test below deliberately
  // uses a DIFFERENT root_dir from this default.
  __resetPathAliasesCacheForTests();
  __resetPathVarsCacheForTests();
  vi.mocked(api.getConfig).mockResolvedValue({ root_dir: '', path_aliases: ALIASES } as Config);
});

function candidate(over: Partial<DedupCandidate> = {}): DedupCandidate {
  return {
    id: 1,
    entity_type: 'book',
    entity_a_id: 'a1',
    entity_b_id: 'b1',
    layer: 'embedding',
    status: 'pending',
    band: 'CERTAIN',
    book_a: { id: 'a1', title: 'Book A' },
    book_b: { id: 'b1', title: 'Book B' },
    ...over,
  } as unknown as DedupCandidate;
}

// Signals in the REAL wire shape (models.Signal JSON tags). There is no
// `primary` on the wire -- the chips derive it from the kind -- so callers pass
// kinds and the primary/supporting split follows from them.
function withSignals(kinds: string[]): DedupCandidate {
  return candidate({
    score_breakdown: {
      score: 98,
      band: 'CERTAIN',
      formula: 'v2',
      signals: kinds.map((kind) => ({
        kind,
        raw: 1,
        confidence: 0.95,
        evidence: `${kind} fired`,
      })),
    },
  });
}

function ctx(): DupesSpineContext {
  return {
    isSelected: () => false,
    onToggleSelect: vi.fn(),
    onAction: vi.fn(),
    focusedId: null,
    expandedId: null,
    onToggleExpand: vi.fn(),
    onOpenCompare: vi.fn(),
  };
}

function renderSpine(candidates: DedupCandidate[], viewMode: 'compact' | 'two-column' = 'compact') {
  return render(
    <MemoryRouter>
      <ThemeProvider theme={appTheme}>
        <DupesSpine
          candidates={candidates}
          viewMode={viewMode}
          ctx={ctx()}
          emptyMessage="Nothing here"
        />
      </ThemeProvider>
    </MemoryRouter>
  );
}

describe('signal chips on a dupes row', () => {
  it('names the primary signal without the row being expanded', () => {
    // The row is compact and NOT expanded -- the evidence section renders only
    // when expanded or two-column, so this asserts the reviewer can read the
    // justification from the row itself.
    renderSpine([withSignals(['exact_file'])]);

    expect(screen.queryByTestId('evidence-section')).toBeNull();
    // "Exact file hash", from the one shared label map. The chip said
    // "exact file" here while the panel said "Exact file hash" until the three
    // copies of the map were consolidated on 2026-09-01.
    expect(screen.getByTestId('signal-chip-exact_file')).toHaveTextContent('Exact file hash');
  });

  it('renders one chip per primary signal', () => {
    renderSpine([withSignals(['isbn_asin', 'metadata_hash'])]);

    expect(screen.getByTestId('signal-chip-isbn_asin')).toHaveTextContent('ISBN/ASIN');
    expect(screen.getByTestId('signal-chip-metadata_hash')).toHaveTextContent('Metadata hash');
  });

  it('does not render supporting signals on the row', () => {
    // A supporting signal can corroborate a pair but never produce one, so a
    // chip beside the primaries would give it weight it cannot earn.
    // Mixed on purpose: a fixture holding only one of the two groups cannot
    // tell a correct filter from an inverted one.
    renderSpine([withSignals(['exact_file', 'duration', 'folder_path'])]);

    expect(screen.getByTestId('signal-chip-exact_file')).toBeInTheDocument();
    expect(screen.queryByTestId('signal-chip-duration')).toBeNull();
    expect(screen.queryByTestId('signal-chip-folder_path')).toBeNull();
  });

  it('renders a row that has no breakdown at all', () => {
    // Rows predating the scorer carry no breakdown; the row still has to draw,
    // and the layer chip -- which is a different claim -- must survive.
    renderSpine([candidate()]);

    expect(screen.getByTestId('dupes-spine')).toBeInTheDocument();
    expect(screen.getByText('embedding')).toBeInTheDocument();
    expect(screen.queryByTestId('signal-chip-exact_file')).toBeNull();
  });

  it('shows an unrecognised kind under its raw name rather than blank', () => {
    renderSpine([withSignals(['some_future_collector'])]);

    expect(screen.getByTestId('signal-chip-some_future_collector')).toHaveTextContent(
      'some_future_collector'
    );
  });
});

describe('dual-path display on a dupes row', () => {
  it('renders a monospace path row with its copy button when file_path is set', () => {
    renderSpine([
      candidate({
        book_a: { id: 'a1', title: 'Book A', file_path: '/library/books/a.m4b' },
      } as unknown as Partial<DedupCandidate>),
    ]);

    const copyButton = screen.getByRole('button', { name: 'Copy Linux path' });
    expect(copyButton).toBeInTheDocument();
    const pathText = screen.getByText('/library/books/a.m4b');
    expect(getComputedStyle(pathText).fontFamily).toBe('monospace');
  });

  it('threads pathAliases through so the Windows and UNC rows actually render', async () => {
    // The POSIX row renders even with an empty aliases array (see the test
    // above, and PathLinks's own renderPath, which always emits it), so a
    // test that only checked for the POSIX row would still pass if DupesSpine
    // never wired usePathAliases() through CandidateRow to BookSide at all --
    // that is exactly the regression this test exists to catch. Only the
    // Windows/UNC rows are proof the alias actually reached PathLinks and
    // matched. usePathAliases() resolves asynchronously (a useEffect around
    // the mocked getConfig() promise), so this awaits the rows rather than
    // asserting synchronously.
    renderSpine([
      candidate({
        book_a: { id: 'a1', title: 'Book A', file_path: '/library/books/Author/Title/x.m4b' },
      } as unknown as Partial<DedupCandidate>),
    ]);

    expect(await screen.findByRole('button', { name: 'Copy Windows path' })).toBeInTheDocument();
    expect(screen.getByRole('button', { name: 'Copy UNC path' })).toBeInTheDocument();
  });

  it('threads pathVars through so the POSIX row renders the ABBREVIATED path', async () => {
    // The twin of the aliases test above, for the other half of PathLinks's
    // inputs. usePathVars() used to be called inside PathLinks itself; it is
    // now hoisted to DupesSpine and threaded down as `vars`, which means a
    // wiring mistake -- threading [] , or forgetting the prop on one of the two
    // BookSides -- is INVISIBLE to every other test in this file: an
    // unabbreviated path still renders, still has a copy button, and still
    // matches every existing assertion. Only the $(libroot) token proves the
    // vars actually arrived.
    vi.mocked(api.getConfig).mockResolvedValue({
      root_dir: '/library/books',
      path_aliases: ALIASES,
    } as Config);

    renderSpine([
      candidate({
        book_a: { id: 'a1', title: 'Book A', file_path: '/library/books/Author/Title/x.m4b' },
        book_b: { id: 'b1', title: 'Book B', file_path: '/library/books/Other/Title/y.m4b' },
      } as unknown as Partial<DedupCandidate>),
    ]);

    // Both sides, so dropping the prop from one BookSide is caught too.
    expect(await screen.findByText('$(libroot)/Author/Title/x.m4b')).toBeInTheDocument();
    expect(screen.getByText('$(libroot)/Other/Title/y.m4b')).toBeInTheDocument();
  });

  it('renders no path row, and does not crash, when the book is missing', () => {
    renderSpine([candidate({ book_a: null } as unknown as Partial<DedupCandidate>)]);

    expect(screen.getByTestId('dupes-spine')).toBeInTheDocument();
    expect(screen.queryByRole('button', { name: /copy/i })).not.toBeInTheDocument();
  });

  it('renders no path row, and does not crash, when file_path is absent', () => {
    // The default candidate() fixture has book_a/book_b with no file_path.
    renderSpine([candidate()]);

    expect(screen.getByTestId('dupes-spine')).toBeInTheDocument();
    expect(screen.queryByRole('button', { name: /copy/i })).not.toBeInTheDocument();
  });
});

// The three MUI <Tooltip>s that this perf change converted to native `title`
// attributes have no other coverage: the Recommended chip is found elsewhere by
// data-testid and the Files chip has no test file at all, so a dropped `title`
// (e.g. if a future MUI major stopped spreading unknown props onto the Chip
// root) would take the hover hint away with every other test still green.
describe('hover hints that replaced MUI Tooltips', () => {
  it('keeps a title on the Recommended chip', () => {
    // book_a carries an asin, so metadataQuality scores it above book_b and
    // recommendedKeepSide returns A -- on a tie the chip renders nothing and
    // this test would pass vacuously.
    renderSpine([
      candidate({
        book_a: { id: 'a1', title: 'Book A', asin: 'B00TEST' },
        book_b: { id: 'b1', title: 'Book B' },
      } as unknown as Partial<DedupCandidate>),
    ]);

    expect(screen.getByTestId('recommended-keep')).toHaveAttribute(
      'title',
      'Richer metadata, so this side is recommended to keep'
    );
  });

  it('keeps a title on the Files chip', () => {
    renderSpine([candidate()]);

    // One per side of the pair.
    const chips = screen.getAllByTitle('Show files in this book');
    expect(chips).toHaveLength(2);
  });
});
