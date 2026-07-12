// file: web/src/components/settings/MetadataScoringSection.test.tsx
// version: 1.1.0
// guid: c7d6e5f4-a3b2-1098-cdef-a98765432109
// last-edited: 2026-07-11

import { render, screen, fireEvent } from '@testing-library/react';
import { describe, it, expect, vi } from 'vitest';
import { MetadataScoringSection } from './MetadataScoringSection';
import type { MetadataScoringConfig } from '../../services/api';

const defaultConfig: MetadataScoringConfig = {
  embedding_enabled: false,
  embedding_min_score: 0.5,
  embedding_best_match: 0.85,
  llm_enabled: false,
  llm_rerank_epsilon: 0.05,
  llm_rerank_top_k: 5,
  write_backup_before: true,
  // new scoring knobs, populated so every field renders with a value
  transcription_title_exact_boost: 2.0,
  transcription_title_substr_boost: 1.4,
  transcription_author_boost: 1.6,
  transcription_narrator_boost: 1.4,
  compilation_penalty: 0.15,
  rich_metadata_field_bonus: 0.05,
  rich_metadata_bonus_cap: 0.15,
  f1_min_score: 0.35,
  series_name_match_boost: 1.4,
  series_number_exact_boost: 2.0,
  series_number_wrong_penalty: 0.5,
  duration_tier_multipliers: [1.3, 1.2, 1.1, 1.0, 0.75, 0.5],
  duration_tier_scores: [20, 15, 10, 0, -10, -20],
  bulk_fetch_workers: 4,
};

describe('MetadataScoringSection', () => {
  it('renders the section heading', () => {
    const onChange = vi.fn();
    render(<MetadataScoringSection config={defaultConfig} onChange={onChange} />);
    expect(screen.getByText('Metadata Scoring')).toBeInTheDocument();
  });

  it('renders the embedding enabled label', () => {
    const onChange = vi.fn();
    render(<MetadataScoringSection config={defaultConfig} onChange={onChange} />);
    expect(
      screen.getByText('Use embedding similarity in metadata scoring')
    ).toBeInTheDocument();
  });

  it('renders the embedding min score field', () => {
    const onChange = vi.fn();
    render(<MetadataScoringSection config={defaultConfig} onChange={onChange} />);
    expect(screen.getByLabelText('Embedding min score')).toBeInTheDocument();
  });

  it('calls onChange with embedding_enabled: true when Switch is toggled on', () => {
    const onChange = vi.fn();
    render(<MetadataScoringSection config={defaultConfig} onChange={onChange} />);
    const switchInput = screen.getByRole('checkbox', {
      name: /use embedding similarity in metadata scoring/i,
    });
    fireEvent.click(switchInput);
    expect(onChange).toHaveBeenCalledWith({ embedding_enabled: true });
  });

  it('calls onChange with llm_enabled: true when LLM Switch is toggled on', () => {
    const onChange = vi.fn();
    render(<MetadataScoringSection config={defaultConfig} onChange={onChange} />);
    const switchInput = screen.getByRole('checkbox', {
      name: /use llm to rerank top candidates/i,
    });
    fireEvent.click(switchInput);
    expect(onChange).toHaveBeenCalledWith({ llm_enabled: true });
  });

  it('calls onChange with numeric embedding_min_score when field changes', () => {
    const onChange = vi.fn();
    render(<MetadataScoringSection config={defaultConfig} onChange={onChange} />);
    const field = screen.getByLabelText('Embedding min score');
    fireEvent.change(field, { target: { value: '0.6' } });
    expect(onChange).toHaveBeenCalledWith({ embedding_min_score: 0.6 });
    const callArg = onChange.mock.calls[0][0] as { embedding_min_score: unknown };
    expect(typeof callArg.embedding_min_score).toBe('number');
  });

  it('calls onChange with numeric embedding_best_match when field changes', () => {
    const onChange = vi.fn();
    render(<MetadataScoringSection config={defaultConfig} onChange={onChange} />);
    const field = screen.getByLabelText('Embedding best match threshold');
    fireEvent.change(field, { target: { value: '0.9' } });
    expect(onChange).toHaveBeenCalledWith({ embedding_best_match: 0.9 });
    const callArg = onChange.mock.calls[0][0] as { embedding_best_match: unknown };
    expect(typeof callArg.embedding_best_match).toBe('number');
  });

  // --- new scoring knobs ---

  it('renders the four transcription boost inputs', () => {
    const onChange = vi.fn();
    render(<MetadataScoringSection config={defaultConfig} onChange={onChange} />);
    expect(screen.getByLabelText('Title exact-match boost')).toBeInTheDocument();
    expect(screen.getByLabelText('Title substring boost')).toBeInTheDocument();
    expect(screen.getByLabelText('Author boost')).toBeInTheDocument();
    expect(screen.getByLabelText('Narrator boost')).toBeInTheDocument();
  });

  it('round-trips a numeric transcription boost edit', () => {
    const onChange = vi.fn();
    render(<MetadataScoringSection config={defaultConfig} onChange={onChange} />);
    fireEvent.change(screen.getByLabelText('Title exact-match boost'), {
      target: { value: '2.5' },
    });
    expect(onChange).toHaveBeenCalledWith({ transcription_title_exact_boost: 2.5 });
    const arg = onChange.mock.calls[0][0] as { transcription_title_exact_boost: unknown };
    expect(typeof arg.transcription_title_exact_boost).toBe('number');
  });

  it('round-trips a numeric series boost edit', () => {
    const onChange = vi.fn();
    render(<MetadataScoringSection config={defaultConfig} onChange={onChange} />);
    fireEvent.change(screen.getByLabelText('Series name match boost'), {
      target: { value: '1.8' },
    });
    expect(onChange).toHaveBeenCalledWith({ series_name_match_boost: 1.8 });
  });

  it('round-trips a numeric bulk-fetch workers edit', () => {
    const onChange = vi.fn();
    render(<MetadataScoringSection config={defaultConfig} onChange={onChange} />);
    fireEvent.change(screen.getByLabelText('Bulk fetch workers'), {
      target: { value: '8' },
    });
    expect(onChange).toHaveBeenCalledWith({ bulk_fetch_workers: 8 });
  });

  it('rebuilds the duration multiplier array when one tier is edited', () => {
    const onChange = vi.fn();
    render(<MetadataScoringSection config={defaultConfig} onChange={onChange} />);
    // First "Tier 1" input belongs to the Multipliers group.
    const tier1 = screen.getAllByLabelText('Tier 1')[0];
    fireEvent.change(tier1, { target: { value: '1.5' } });
    const arg = onChange.mock.calls[0][0] as { duration_tier_multipliers?: number[] };
    expect(arg.duration_tier_multipliers).toEqual([1.5, 1.2, 1.1, 1.0, 0.75, 0.5]);
  });

  it('does not emit NaN when a numeric field is cleared', () => {
    const onChange = vi.fn();
    render(<MetadataScoringSection config={defaultConfig} onChange={onChange} />);
    fireEvent.change(screen.getByLabelText('Author boost'), { target: { value: '' } });
    const arg = onChange.mock.calls[0][0] as { transcription_author_boost: unknown };
    expect(arg.transcription_author_boost).toBeUndefined();
    // Sanity: not NaN.
    expect(Number.isNaN(arg.transcription_author_boost as number)).toBe(false);
  });

  // Pointer-knob semantics (spec C2): empty → absent (undefined), explicit 0 → 0.
  it('sends an empty pointer knob (f1_min_score) as absent, never 0', () => {
    const onChange = vi.fn();
    render(<MetadataScoringSection config={defaultConfig} onChange={onChange} />);
    fireEvent.change(screen.getByLabelText('F1 minimum score'), { target: { value: '' } });
    const arg = onChange.mock.calls[0][0] as { f1_min_score?: number };
    expect(arg.f1_min_score).toBeUndefined();
    expect(arg.f1_min_score).not.toBe(0);
  });

  it('sends an explicit 0 for a pointer knob (compilation_penalty) as 0', () => {
    const onChange = vi.fn();
    render(<MetadataScoringSection config={defaultConfig} onChange={onChange} />);
    fireEvent.change(screen.getByLabelText('Compilation penalty'), { target: { value: '0' } });
    expect(onChange).toHaveBeenCalledWith({ compilation_penalty: 0 });
    const arg = onChange.mock.calls[0][0] as { compilation_penalty: unknown };
    expect(arg.compilation_penalty).toBe(0);
  });

  it('resets transcription boosts to defaults', () => {
    const onChange = vi.fn();
    render(<MetadataScoringSection config={defaultConfig} onChange={onChange} />);
    fireEvent.click(screen.getByText('Reset transcription boosts to defaults'));
    expect(onChange).toHaveBeenCalledWith({
      transcription_title_exact_boost: 2.0,
      transcription_title_substr_boost: 1.4,
      transcription_author_boost: 1.6,
      transcription_narrator_boost: 1.4,
    });
  });
});
