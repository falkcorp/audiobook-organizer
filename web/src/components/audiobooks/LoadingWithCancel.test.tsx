// file: web/src/components/audiobooks/LoadingWithCancel.test.tsx
// version: 1.0.0
// guid: 7a1e3c5b-9d2f-4a6c-8b1e-3d5f7a9c1b3e
// last-edited: 2026-07-11

import { render, screen, act } from '@testing-library/react';
import { describe, test, expect, vi, beforeEach, afterEach } from 'vitest';
import { LoadingWithCancel } from './LoadingWithCancel';

describe('LoadingWithCancel', () => {
  beforeEach(() => {
    vi.useFakeTimers();
  });

  afterEach(() => {
    vi.useRealTimers();
  });

  test('does not show a cancel button before the slow threshold', () => {
    render(<LoadingWithCancel onCancel={vi.fn()} />);
    expect(screen.queryByRole('button', { name: /cancel/i })).not.toBeInTheDocument();
  });

  test('shows a cancel button after the slow threshold and calls onCancel when clicked', () => {
    const onCancel = vi.fn();
    render(<LoadingWithCancel onCancel={onCancel} />);

    act(() => {
      vi.advanceTimersByTime(3000);
    });

    const button = screen.getByRole('button', { name: /cancel/i });
    button.click();
    expect(onCancel).toHaveBeenCalledTimes(1);
  });

  test('never shows a cancel button when onCancel is not provided', () => {
    render(<LoadingWithCancel />);

    act(() => {
      vi.advanceTimersByTime(5000);
    });

    expect(screen.queryByRole('button', { name: /cancel/i })).not.toBeInTheDocument();
  });
});
