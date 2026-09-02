// file: web/src/components/settings/DedupSettingsSection.test.tsx
// version: 1.1.0
// guid: b8c7d6e5-f4a3-2109-bcde-fa8765432109
// last-edited: 2026-09-02

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
    band_certain_min: 96,
    band_high_min: 85.5,
    band_medium_min: 75,
    band_review_min: 60,
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
    const switchInput = screen.getByRole('switch', { name: /auto-merge certain duplicates/i });
    fireEvent.click(switchInput);
    expect(onChange).toHaveBeenCalledWith({ auto_merge_enabled: true });
  });

  it('calls onChange with embeddings_enabled: true when embeddings Switch is toggled', () => {
    const onChange = vi.fn();
    render(<DedupSettingsSection config={defaultConfig} onChange={onChange} />);
    const switchInput = screen.getByRole('switch', {
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

  // The composite-score bands are on the server's 0–100 scale. Until 2026-09-02
  // these inputs were bounded 0–1 step 0.01, so the browser's own validation
  // flagged every real value (96, 85.5, ...) as out of range and the arrow keys
  // stepped in hundredths of a point.
  it.each([
    ['Certain band min', 96],
    ['High band min', 85.5],
    ['Medium band min', 75],
    ['Review band min', 60],
  ])('renders %s on the 0–100 scale with a 0.5 step', (label, value) => {
    render(<DedupSettingsSection config={defaultConfig} onChange={vi.fn()} />);
    const field = screen.getByLabelText(label) as HTMLInputElement;
    expect(field).toHaveValue(value);
    expect(field).toHaveAttribute('min', '0');
    expect(field).toHaveAttribute('max', '100');
    expect(field).toHaveAttribute('step', '0.5');
  });

  it('calls onChange with a numeric 0–100 band_certain_min when the field changes', () => {
    const onChange = vi.fn();
    render(<DedupSettingsSection config={defaultConfig} onChange={onChange} />);
    fireEvent.change(screen.getByLabelText('Certain band min'), { target: { value: '98.5' } });
    const callArg = onChange.mock.calls[0][0] as { signals: { band_certain_min: unknown } };
    expect(callArg.signals.band_certain_min).toBe(98.5);
  });
});
