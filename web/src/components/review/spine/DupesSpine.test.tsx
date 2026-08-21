// file: web/src/components/review/spine/DupesSpine.test.tsx
// version: 1.0.0
// guid: 2b6d9e40-51c8-4a37-8f92-c704a1d5e836
// last-edited: 2026-08-20
//
// Covers the signal chips on a dupes row.
//
// Written because a mutation exposed the gap: deleting the entire chip-render
// block left all 687 other tests green. `primarySignals` was well covered as a
// function, which proves nothing about whether a chip reaches the screen -- and
// reaching the screen is the whole feature, since its purpose is to answer
// "why is this pair here" WITHOUT the reviewer expanding anything.

import { describe, expect, it, vi } from 'vitest';
import { render, screen } from '@testing-library/react';
import { ThemeProvider } from '@mui/material/styles';
import { MemoryRouter } from 'react-router-dom';
import { DupesSpine, type DupesSpineContext } from './DupesSpine';
import { appTheme } from '../../../theme';
import type { DedupCandidate } from '../../../services/api';

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

function withSignals(sigs: Array<{ kind: string; primary: boolean }>): DedupCandidate {
  return candidate({
    score_breakdown: {
      score: 98,
      signals: sigs.map((s) => ({ kind: s.kind, value: 1, weight: 1, primary: s.primary })),
    },
  } as unknown as Partial<DedupCandidate>);
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
    renderSpine([withSignals([{ kind: 'exact_file', primary: true }])]);

    expect(screen.queryByTestId('evidence-section')).toBeNull();
    expect(screen.getByTestId('signal-chip-exact_file')).toHaveTextContent('exact file');
  });

  it('renders one chip per primary signal', () => {
    renderSpine([
      withSignals([
        { kind: 'isbn_asin', primary: true },
        { kind: 'metadata_hash', primary: true },
      ]),
    ]);

    expect(screen.getByTestId('signal-chip-isbn_asin')).toHaveTextContent('ISBN/ASIN');
    expect(screen.getByTestId('signal-chip-metadata_hash')).toHaveTextContent(
      'same source record'
    );
  });

  it('does not render supporting signals on the row', () => {
    // A supporting signal can corroborate a pair but never produce one, so a
    // chip beside the primaries would give it weight it cannot earn.
    renderSpine([
      withSignals([
        { kind: 'exact_file', primary: true },
        { kind: 'duration', primary: false },
        { kind: 'folder_path', primary: false },
      ]),
    ]);

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
    renderSpine([withSignals([{ kind: 'some_future_collector', primary: true }])]);

    expect(screen.getByTestId('signal-chip-some_future_collector')).toHaveTextContent(
      'some_future_collector'
    );
  });
});
