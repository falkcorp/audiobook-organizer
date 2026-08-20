// file: web/src/components/common/FilterTagBar.test.tsx
// version: 1.0.1
// guid: 8d5e9f23-4c7a-5b8d-aef2-1b3c4d5e6f7a

import { render, screen, fireEvent } from '@testing-library/react';
import { describe, it, expect, vi } from 'vitest';
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

  it('fires onRemove when a chip delete icon is clicked', () => {
    const onRemove = vi.fn();
    render(<FilterTagBar tags={[{ id: 'status:pending', label: 'Status: pending', onRemove }]} />);
    // MUI Chip exposes the delete icon with role=button via test-id-friendly
    // markup; click the SVG icon that lives inside the chip.
    const deleteIcons = document.querySelectorAll('.MuiChip-deleteIcon');
    expect(deleteIcons.length).toBe(1);
    fireEvent.click(deleteIcons[0]);
    expect(onRemove).toHaveBeenCalledTimes(1);
  });

  it('shows Clear all only when ≥2 tags and a handler is provided', () => {
    const onClearAll = vi.fn();
    const oneTag = [{ id: 'status:pending', label: 'Status: pending', onRemove: vi.fn() }];
    const twoTags = [...oneTag, { id: 'layer:exact', label: 'Layer: exact', onRemove: vi.fn() }];

    const { rerender } = render(<FilterTagBar tags={oneTag} onClearAll={onClearAll} />);
    expect(screen.queryByText('Clear all')).not.toBeInTheDocument();

    rerender(<FilterTagBar tags={twoTags} onClearAll={onClearAll} />);
    const clearAll = screen.getByText('Clear all');
    fireEvent.click(clearAll);
    expect(onClearAll).toHaveBeenCalledTimes(1);
  });

  it('hides the Clear all button when no handler is supplied', () => {
    render(
      <FilterTagBar
        tags={[
          { id: 'status:pending', label: 'Status: pending', onRemove: vi.fn() },
          { id: 'layer:exact', label: 'Layer: exact', onRemove: vi.fn() },
        ]}
      />
    );
    expect(screen.queryByText('Clear all')).not.toBeInTheDocument();
  });
});
