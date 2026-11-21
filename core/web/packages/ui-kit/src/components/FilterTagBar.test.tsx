// file: core/web/packages/ui-kit/src/components/FilterTagBar.test.tsx
// version: 0.1.0
// guid: 23837c77-5e01-4345-b5c0-c7584bc54877
// last-edited: 2026-11-21

import '@testing-library/jest-dom/vitest';
import { fireEvent, render, screen } from '@testing-library/react';
import { describe, expect, it, vi } from 'vitest';
import { FilterTagBar } from './FilterTagBar';

describe('FilterTagBar', () => {
  it('renders nothing when there are no tags', () => {
    const { container } = render(<FilterTagBar tags={[]} />);
    expect(container).toBeEmptyDOMElement();
  });

  it('renders one chip per tag with the supplied label', () => {
    const tags = [
      { id: 'status:pending', label: 'Status: pending', onRemove: vi.fn() },
      { id: 'layer:exact', label: 'Layer: exact', onRemove: vi.fn() },
    ];
    render(<FilterTagBar tags={tags} />);
    expect(screen.getByText('Status: pending')).toBeInTheDocument();
    expect(screen.getByText('Layer: exact')).toBeInTheDocument();
  });

  it('fires onRemove for the chip whose delete icon is clicked, and only that one', () => {
    const removeA = vi.fn();
    const removeB = vi.fn();
    render(
      <FilterTagBar
        tags={[
          { id: 'a', label: 'Villain: Joker', onRemove: removeA },
          { id: 'b', label: 'Villain: Bane', onRemove: removeB },
        ]}
      />,
    );
    const deleteIcons = document.querySelectorAll('.MuiChip-deleteIcon');
    expect(deleteIcons.length).toBe(2);
    fireEvent.click(deleteIcons[1]);
    expect(removeB).toHaveBeenCalledTimes(1);
    expect(removeA).not.toHaveBeenCalled();
  });

  it('shows Clear all only with at least two tags and a handler', () => {
    const onClearAll = vi.fn();
    const oneTag = [{ id: 'status:pending', label: 'Status: pending', onRemove: vi.fn() }];
    const twoTags = [...oneTag, { id: 'layer:exact', label: 'Layer: exact', onRemove: vi.fn() }];

    const { rerender } = render(<FilterTagBar tags={oneTag} onClearAll={onClearAll} />);
    expect(screen.queryByText('Clear all')).not.toBeInTheDocument();

    rerender(<FilterTagBar tags={twoTags} onClearAll={onClearAll} />);
    fireEvent.click(screen.getByText('Clear all'));
    expect(onClearAll).toHaveBeenCalledTimes(1);
  });

  it('hides Clear all when no handler is supplied', () => {
    render(
      <FilterTagBar
        tags={[
          { id: 'a', label: 'Status: pending', onRemove: vi.fn() },
          { id: 'b', label: 'Layer: exact', onRemove: vi.fn() },
        ]}
      />,
    );
    expect(screen.queryByText('Clear all')).not.toBeInTheDocument();
  });

  it('shows the prefix label, overridable and hideable', () => {
    const tags = [{ id: 'a', label: 'Status: pending', onRemove: vi.fn() }];
    const { rerender } = render(<FilterTagBar tags={tags} />);
    expect(screen.getByText('Filters:')).toBeInTheDocument();
    rerender(<FilterTagBar tags={tags} label="Active:" />);
    expect(screen.getByText('Active:')).toBeInTheDocument();
    rerender(<FilterTagBar tags={tags} hideLabel />);
    expect(screen.queryByText('Filters:')).not.toBeInTheDocument();
  });
});
