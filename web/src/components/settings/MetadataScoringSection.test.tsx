// file: web/src/components/settings/MetadataScoringSection.test.tsx
// version: 1.0.0
// guid: c7d6e5f4-a3b2-1098-cdef-a98765432109
// last-edited: 2026-06-19

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
});
