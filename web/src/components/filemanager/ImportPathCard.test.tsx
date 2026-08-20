// file: src/components/filemanager/ImportPathCard.test.tsx
// version: 1.0.1
// guid: a4b5c6d7-e8f9-0a1b-2c3d-4e5f6a7b8c9d

import { render, screen, fireEvent } from '@testing-library/react';
import { describe, it, expect, vi } from 'vitest';
import { ImportPathCard, ImportPath } from './ImportPathCard';

const buildPath = (overrides: Partial<ImportPath> = {}): ImportPath => ({
  id: 1,
  path: '/media/import',
  status: 'idle',
  book_count: 0,
  progress: undefined,
  last_scan: undefined,
  error_message: undefined,
  ...overrides,
});

describe('ImportPathCard', () => {
  it('renders import path details', () => {
    render(<ImportPathCard importPath={buildPath()} />);

    expect(screen.getByText('import')).toBeInTheDocument();
    expect(screen.getByText('/media/import')).toBeInTheDocument();
  });

  it('invokes callbacks for scan and remove', () => {
    const onScan = vi.fn();
    const onRemove = vi.fn();
    render(<ImportPathCard importPath={buildPath()} onScan={onScan} onRemove={onRemove} />);

    fireEvent.click(screen.getByLabelText('import path actions'));
    fireEvent.click(screen.getByText('Scan Now'));
    expect(onScan).toHaveBeenCalledTimes(1);

    fireEvent.click(screen.getByLabelText('import path actions'));
    fireEvent.click(screen.getByText('Remove'));
    expect(onRemove).toHaveBeenCalledTimes(1);
  });
});
