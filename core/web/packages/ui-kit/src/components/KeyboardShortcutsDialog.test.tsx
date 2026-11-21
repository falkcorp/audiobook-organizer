// file: core/web/packages/ui-kit/src/components/KeyboardShortcutsDialog.test.tsx
// version: 0.1.0
// guid: da8c0327-a34a-4c4d-b341-a12d88e15ad0
// last-edited: 2026-11-21

import '@testing-library/jest-dom/vitest';
import { fireEvent, render, screen } from '@testing-library/react';
import { describe, expect, it, vi } from 'vitest';
import { KeyboardShortcutsDialog, type ShortcutHelpEntry } from './KeyboardShortcutsDialog';

const TABLE: ShortcutHelpEntry[] = [
  { keys: '/ or Ctrl+K', description: 'Focus search input', category: 'Navigation' },
  { keys: 'g w', description: 'Go to Wayne Manor', category: 'Navigation' },
  { keys: '?', description: 'Show keyboard shortcuts', category: 'General' },
];

describe('KeyboardShortcutsDialog', () => {
  it('lists exactly the table it is given, grouped by category in first-seen order', () => {
    render(<KeyboardShortcutsDialog open onClose={() => {}} shortcuts={TABLE} />);
    const headings = screen.getAllByText(/^(Navigation|General)$/).map((e) => e.textContent);
    expect(headings).toEqual(['Navigation', 'General']);
    for (const s of TABLE) expect(screen.getByText(s.description)).toBeInTheDocument();
  });

  it('shows each alternative of an "or" binding as its own chip', () => {
    render(<KeyboardShortcutsDialog open onClose={() => {}} shortcuts={TABLE} />);
    expect(screen.getByText('/')).toBeInTheDocument();
    expect(screen.getByText('Ctrl+K')).toBeInTheDocument();
    expect(screen.getByText('g w')).toBeInTheDocument();
  });

  it('names no route of its own: an empty table renders no rows', () => {
    render(<KeyboardShortcutsDialog open onClose={() => {}} shortcuts={[]} />);
    expect(screen.getByText('Keyboard Shortcuts')).toBeInTheDocument();
    expect(screen.queryByText(/Go to/)).not.toBeInTheDocument();
  });

  it('closes from the close button', () => {
    const onClose = vi.fn();
    render(<KeyboardShortcutsDialog open onClose={onClose} shortcuts={TABLE} />);
    fireEvent.click(screen.getByLabelText('close'));
    expect(onClose).toHaveBeenCalledTimes(1);
  });
});
