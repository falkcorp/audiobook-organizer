// file: web/src/components/dedup/__tests__/LabelToggle.test.tsx
// version: 1.0.0
// guid: 2a9d7e14-5c83-4b60-9f02-6e1a8c3d5b47
// last-edited: 2026-06-19

import { describe, it, expect, vi } from 'vitest';
import { render, screen, fireEvent } from '@testing-library/react';
import { LabelToggle } from '../LabelToggle';

describe('LabelToggle', () => {
  it('renders the three label options', () => {
    render(<LabelToggle value="" onChange={() => {}} />);
    expect(screen.getByRole('button', { name: /^dup$/i })).toBeInTheDocument();
    expect(screen.getByRole('button', { name: /unsure/i })).toBeInTheDocument();
    expect(screen.getByRole('button', { name: /^not$/i })).toBeInTheDocument();
  });

  it('marks the current value as pressed', () => {
    render(<LabelToggle value="true_dup" onChange={() => {}} />);
    expect(screen.getByRole('button', { name: /^dup$/i })).toHaveAttribute('aria-pressed', 'true');
    expect(screen.getByRole('button', { name: /^not$/i })).toHaveAttribute('aria-pressed', 'false');
  });

  it('calls onChange with the clicked value', () => {
    const onChange = vi.fn();
    render(<LabelToggle value="" onChange={onChange} />);
    fireEvent.click(screen.getByRole('button', { name: /^not$/i }));
    expect(onChange).toHaveBeenCalledWith('not_dup');
  });

  it('does not call onChange when clicking the already-selected value', () => {
    const onChange = vi.fn();
    render(<LabelToggle value="not_dup" onChange={onChange} />);
    fireEvent.click(screen.getByRole('button', { name: /^not$/i }));
    expect(onChange).not.toHaveBeenCalled();
  });
});
