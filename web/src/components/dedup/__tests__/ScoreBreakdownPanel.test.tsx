// file: web/src/components/dedup/__tests__/ScoreBreakdownPanel.test.tsx
// version: 1.0.1
// guid: b2c3d4e5-f6a7-8901-bcde-222345678901
// last-edited: 2026-08-19

import { render, screen } from '@testing-library/react';
import { describe, it, expect } from 'vitest';
import { ScoreBreakdownPanel } from '../ScoreBreakdownPanel';
import type { DedupScoreBreakdown } from '../../../services/api';

const mockBreakdown: DedupScoreBreakdown = {
  score: 97.5,
  band: 'CERTAIN',
  formula: 'v2',
  signals: [
    {
      kind: 'exact_file',
      value: 1.0,
      weight: 100,
      evidence: 'Exact file hash match',
      primary: true,
    },
    {
      kind: 'embedding_high',
      value: 0.95,
      weight: 80,
      evidence: 'High embedding similarity',
      primary: true,
    },
    {
      kind: 'duration',
      value: 0.98,
      weight: 20,
      evidence: 'Duration within 2%',
      primary: false,
    },
  ],
};

describe('ScoreBreakdownPanel', () => {
  it('renders the score and band', () => {
    render(<ScoreBreakdownPanel breakdown={mockBreakdown} />);
    expect(screen.getByText(/Score: 97\.5/)).toBeInTheDocument();
    expect(screen.getByText('CERTAIN')).toBeInTheDocument();
  });

  it('renders the stacked bar', () => {
    render(<ScoreBreakdownPanel breakdown={mockBreakdown} />);
    expect(screen.getByTestId('score-stacked-bar')).toBeInTheDocument();
  });

  it('renders signal rows', () => {
    render(<ScoreBreakdownPanel breakdown={mockBreakdown} />);
    expect(screen.getByText('Exact file hash')).toBeInTheDocument();
    expect(screen.getByText('Embedding (high)')).toBeInTheDocument();
    expect(screen.getByText('Duration match')).toBeInTheDocument();
  });

  it('renders formula tag', () => {
    render(<ScoreBreakdownPanel breakdown={mockBreakdown} />);
    expect(screen.getByText('v2')).toBeInTheDocument();
  });

  it('renders empty state when no signals', () => {
    render(<ScoreBreakdownPanel breakdown={{ ...mockBreakdown, signals: [] }} />);
    expect(screen.getByText(/No signal data available/i)).toBeInTheDocument();
  });

  it('renders skipped_reason when present', () => {
    render(
      <ScoreBreakdownPanel
        breakdown={{ ...mockBreakdown, signals: [], skipped_reason: 'pre-T015 candidate' }}
      />
    );
    expect(screen.getByText(/pre-T015 candidate/i)).toBeInTheDocument();
  });
});
