// file: web/src/components/dedup/__tests__/BandFilterBar.test.tsx
// version: 1.0.1
// guid: a1b2c3d4-e5f6-7890-abcd-111234567890
// last-edited: 2026-08-19

import { render, screen, fireEvent } from '@testing-library/react';
import { describe, it, expect, vi } from 'vitest';
import { BandFilterBar } from '../BandFilterBar';
import type { BandCounts } from '../BandFilterBar';
import type { DedupBand } from '../../../services/api';

const mockCounts: BandCounts = {
  CERTAIN: 5,
  HIGH: 12,
  MEDIUM: 30,
  REVIEW: 8,
  total: 55,
};

describe('BandFilterBar', () => {
  it('renders all band chips', () => {
    render(<BandFilterBar selected={null} counts={mockCounts} onChange={vi.fn()} />);
    expect(screen.getByTestId('band-chip-CERTAIN')).toBeInTheDocument();
    expect(screen.getByTestId('band-chip-HIGH')).toBeInTheDocument();
    expect(screen.getByTestId('band-chip-MEDIUM')).toBeInTheDocument();
    expect(screen.getByTestId('band-chip-REVIEW')).toBeInTheDocument();
  });

  it('shows counts in chip labels', () => {
    render(<BandFilterBar selected={null} counts={mockCounts} onChange={vi.fn()} />);
    expect(screen.getByText('Certain (5)')).toBeInTheDocument();
    expect(screen.getByText('High (12)')).toBeInTheDocument();
    expect(screen.getByText('All (55)')).toBeInTheDocument();
  });

  it('calls onChange with the selected band on click', () => {
    const onChange = vi.fn();
    render(<BandFilterBar selected={null} counts={mockCounts} onChange={onChange} />);
    fireEvent.click(screen.getByTestId('band-chip-CERTAIN'));
    expect(onChange).toHaveBeenCalledWith('CERTAIN' as DedupBand);
  });

  it('calls onChange with null when clicking the same band again', () => {
    const onChange = vi.fn();
    render(
      <BandFilterBar selected={'CERTAIN' as DedupBand} counts={mockCounts} onChange={onChange} />
    );
    fireEvent.click(screen.getByTestId('band-chip-CERTAIN'));
    expect(onChange).toHaveBeenCalledWith(null);
  });

  it('calls onChange with null when clicking All', () => {
    const onChange = vi.fn();
    render(
      <BandFilterBar selected={'HIGH' as DedupBand} counts={mockCounts} onChange={onChange} />
    );
    fireEvent.click(screen.getByText('All (55)'));
    expect(onChange).toHaveBeenCalledWith(null);
  });
});
