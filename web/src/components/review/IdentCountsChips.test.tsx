// file: web/src/components/review/IdentCountsChips.test.tsx
// version: 1.0.0
// guid: cb9fda36-028f-4113-b148-679501a502f3
// last-edited: 2026-10-11

import { render, screen } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { MemoryRouter, Route, Routes, useLocation } from 'react-router-dom';
import { describe, expect, it } from 'vitest';
import { IdentCountsChips, IDENT_CHIPS, identLibraryHref } from './IdentCountsChips';
import parity from '../../pages/__fixtures__/ident_missing_parity.json';

function LibraryProbe() {
  const loc = useLocation();
  return <div data-testid="library-location">{`${loc.pathname}${loc.search}`}</div>;
}

const COUNTS = {
  unasked: 7,
  asked_empty: 4,
  has_candidates: 2,
  applied: 1,
  held: 1,
  missing: parity.ident_counts_missing,
  ready: true,
};

function renderChips(counts = COUNTS) {
  return render(
    <MemoryRouter initialEntries={['/review']}>
      <Routes>
        <Route path="/review" element={<IdentCountsChips counts={counts} />} />
        <Route path="/library" element={<LibraryProbe />} />
      </Routes>
    </MemoryRouter>
  );
}

describe('IdentCountsChips', () => {
  it('links every count to the Library with its ident: search', () => {
    renderChips();
    for (const { value } of IDENT_CHIPS) {
      const chip = screen.getByTestId(`ident-chip-${value}`);
      expect(chip).toHaveAttribute('href', identLibraryHref(value));
      expect(chip).toHaveTextContent(String(COUNTS[value]));
    }
    expect(identLibraryHref('missing')).toBe('/library?search=ident%3Amissing');
  });

  it('opens the Library on ident:missing when the missing count is clicked', async () => {
    const user = userEvent.setup();
    renderChips();
    expect(screen.getByTestId('ident-chip-missing')).toHaveTextContent(
      `${parity.ident_counts_missing} missing metadata`
    );
    await user.click(screen.getByTestId('ident-chip-missing'));
    expect(screen.getByTestId('library-location')).toHaveTextContent(
      '/library?search=ident%3Amissing'
    );
  });

  it('shows no count while the index is being built', () => {
    renderChips({ ...COUNTS, ready: false, missing: 0 });
    expect(screen.getByTestId('ident-chip-building')).toBeInTheDocument();
    expect(screen.queryByTestId('ident-chip-missing')).toBeNull();
  });
});
