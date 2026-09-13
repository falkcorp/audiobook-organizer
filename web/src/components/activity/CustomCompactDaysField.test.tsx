// file: web/src/components/activity/CustomCompactDaysField.test.tsx
// version: 1.1.0
// guid: 2c9e5a17-8b3f-4d60-a4c2-9e1f7b3d5a28
// last-edited: 2026-09-13

import { describe, it, expect, vi } from 'vitest';
import { render, screen, fireEvent } from '@testing-library/react';
import { useState } from 'react';
import CustomCompactDaysField from './CustomCompactDaysField';
import { COMPACT_DAYS_ERROR, parseCompactDays } from './compactDays';

describe('parseCompactDays', () => {
  it.each([
    '1.75',
    '0.5',
    '0',
    '-3',
    '1e3',
    ' 7 ',
    'abc',
    '',
    '+7',
    '36501',
    '213503982334601',
    '99999999999999999999',
  ])('rejects %j', (raw) => {
    expect(parseCompactDays(raw)).toBeNull();
  });

  it.each([
    ['1', 1],
    ['7', 7],
    ['365', 365],
    ['36500', 36500],
  ])('accepts %j as %d', (raw, want) => {
    expect(parseCompactDays(raw)).toBe(want);
  });
});

function Harness({ onSubmit }: { onSubmit: (days: number) => void }) {
  const [value, setValue] = useState('');
  return <CustomCompactDaysField value={value} onChange={setValue} onSubmit={onSubmit} />;
}

describe('CustomCompactDaysField', () => {
  // type="number": jsdom sanitizes non-numeric values (' 7 ', 'abc') to '', so
  // those rows exercise the empty path here; parseCompactDays above covers the
  // raw strings. '1.75', '0.5', '-3' and '1e3' survive sanitization.
  it.each(['1.75', '0.5', '0', '-3', '1e3', ' 7 ', 'abc', '', '36501', '213503982334601'])(
    'Enter on %j shows the error and does not submit',
    (raw) => {
      const onSubmit = vi.fn();
      render(<Harness onSubmit={onSubmit} />);
      const input = screen.getByPlaceholderText('Custom days');
      fireEvent.change(input, { target: { value: raw } });
      fireEvent.keyDown(input, { key: 'Enter' });
      expect(onSubmit).not.toHaveBeenCalled();
      expect(screen.getByText(COMPACT_DAYS_ERROR)).toBeInTheDocument();
      expect(input).toHaveAttribute('aria-invalid', 'true');
    }
  );

  it('submits a whole number of days', () => {
    const onSubmit = vi.fn();
    render(<Harness onSubmit={onSubmit} />);
    const input = screen.getByPlaceholderText('Custom days');
    fireEvent.change(input, { target: { value: '7' } });
    fireEvent.keyDown(input, { key: 'Enter' });
    expect(onSubmit).toHaveBeenCalledTimes(1);
    expect(onSubmit).toHaveBeenCalledWith(7);
    expect(screen.queryByText(COMPACT_DAYS_ERROR)).not.toBeInTheDocument();
  });

  it('names the range in the error', () => {
    expect(COMPACT_DAYS_ERROR).toBe('Enter a whole number of days from 1 to 36500');
  });

  it('clears the error when the value is edited', () => {
    render(<Harness onSubmit={vi.fn()} />);
    const input = screen.getByPlaceholderText('Custom days');
    fireEvent.change(input, { target: { value: '1.75' } });
    fireEvent.keyDown(input, { key: 'Enter' });
    expect(screen.getByText(COMPACT_DAYS_ERROR)).toBeInTheDocument();
    fireEvent.change(input, { target: { value: '2' } });
    expect(screen.queryByText(COMPACT_DAYS_ERROR)).not.toBeInTheDocument();
  });

  it('constrains the native input to whole days from 1', () => {
    render(<Harness onSubmit={vi.fn()} />);
    const input = screen.getByPlaceholderText('Custom days');
    expect(input).toHaveAttribute('min', '1');
    expect(input).toHaveAttribute('step', '1');
  });
});
