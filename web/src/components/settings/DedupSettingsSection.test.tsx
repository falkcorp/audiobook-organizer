// file: web/src/components/settings/DedupSettingsSection.test.tsx
// version: 1.0.0
// guid: b8c7d6e5-f4a3-2109-bcde-fa8765432109
// last-edited: 2026-06-19

import { render, screen, fireEvent } from '@testing-library/react';
import { describe, it, expect, vi } from 'vitest';
import { DedupSettingsSection } from './DedupSettingsSection';
import type { DedupConfig } from '../../services/api';

const defaultConfig: DedupConfig = {
  book_high_threshold: 0.9,
  book_low_threshold: 0.5,
  author_high_threshold: 0.9,
  author_low_threshold: 0.5,
  auto_merge_enabled: false,
  embeddings_enabled: false,
  llm_auto_merge_high_confidence: false,
  on_import_via_scheduler: false,
  review_model: 'gpt-4o',
  signals: {
    band_certain_min: 0.95,
    band_high_min: 0.85,
    band_medium_min: 0.7,
    band_review_min: 0.5,
    duration_boost: 0.1,
    folder_path_boost: 0.05,
  },
};

describe('DedupSettingsSection', () => {
  it('renders the section heading', () => {
    const onChange = vi.fn();
    render(<DedupSettingsSection config={defaultConfig} onChange={onChange} />);
    expect(screen.getByText('Deduplication Settings')).toBeInTheDocument();
  });

  it('renders the auto-merge label', () => {
    const onChange = vi.fn();
    render(<DedupSettingsSection config={defaultConfig} onChange={onChange} />);
    expect(screen.getByText('Auto-merge certain duplicates')).toBeInTheDocument();
  });

  it('renders the book high threshold field', () => {
    const onChange = vi.fn();
    render(<DedupSettingsSection config={defaultConfig} onChange={onChange} />);
    expect(screen.getByLabelText('Book high threshold')).toBeInTheDocument();
  });

  it('calls onChange with auto_merge_enabled: true when Switch is toggled on', () => {
    const onChange = vi.fn();
    render(<DedupSettingsSection config={defaultConfig} onChange={onChange} />);
    const switchInput = screen.getByRole('checkbox', { name: /auto-merge certain duplicates/i });
    fireEvent.click(switchInput);
    expect(onChange).toHaveBeenCalledWith({ auto_merge_enabled: true });
  });

  it('calls onChange with embeddings_enabled: true when embeddings Switch is toggled', () => {
    const onChange = vi.fn();
    render(<DedupSettingsSection config={defaultConfig} onChange={onChange} />);
    const switchInput = screen.getByRole('checkbox', {
      name: /enable embedding similarity/i,
    });
    fireEvent.click(switchInput);
    expect(onChange).toHaveBeenCalledWith({ embeddings_enabled: true });
  });

  it('calls onChange with numeric book_high_threshold when field changes', () => {
    const onChange = vi.fn();
    render(<DedupSettingsSection config={defaultConfig} onChange={onChange} />);
    const field = screen.getByLabelText('Book high threshold');
    fireEvent.change(field, { target: { value: '0.85' } });
    expect(onChange).toHaveBeenCalledWith({ book_high_threshold: 0.85 });
    const callArg = onChange.mock.calls[0][0] as { book_high_threshold: unknown };
    expect(typeof callArg.book_high_threshold).toBe('number');
  });

  it('calls onChange with numeric book_low_threshold when field changes', () => {
    const onChange = vi.fn();
    render(<DedupSettingsSection config={defaultConfig} onChange={onChange} />);
    const field = screen.getByLabelText('Book low threshold');
    fireEvent.change(field, { target: { value: '0.4' } });
    expect(onChange).toHaveBeenCalledWith({ book_low_threshold: 0.4 });
    const callArg = onChange.mock.calls[0][0] as { book_low_threshold: unknown };
    expect(typeof callArg.book_low_threshold).toBe('number');
  });
});
