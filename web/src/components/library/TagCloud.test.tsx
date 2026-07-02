// file: web/src/components/library/TagCloud.test.tsx
// version: 1.0.0
// guid: 4f3d2c1b-8a9e-4d7c-b6a5-2e1f9c8d7b6a
// last-edited: 2026-07-01

import { describe, it, expect, vi, beforeEach } from 'vitest';
import { screen, fireEvent } from '@testing-library/react';
import { renderWithProviders } from '../../test/renderWithProviders';
import { TagCloud } from './TagCloud';

function defaultProps(overrides: Partial<Parameters<typeof TagCloud>[0]> = {}) {
  return {
    availableTags: [
      { tag: 'fantasy', count: 100 },
      { tag: 'scifi', count: 5 },
      { tag: 'mystery', count: 20 },
    ],
    selectedTags: [] as string[],
    onTagsChange: vi.fn(),
    ...overrides,
  };
}

beforeEach(() => {
  vi.clearAllMocks();
});

describe('TagCloud', () => {
  it('renders a chip for every available tag', () => {
    renderWithProviders(<TagCloud {...defaultProps()} />);
    expect(screen.getByText('fantasy (100)')).toBeInTheDocument();
    expect(screen.getByText('scifi (5)')).toBeInTheDocument();
    expect(screen.getByText('mystery (20)')).toBeInTheDocument();
  });

  it('renders nothing when there are no available tags', () => {
    const { container } = renderWithProviders(<TagCloud {...defaultProps({ availableTags: [] })} />);
    expect(container.firstChild).toBeNull();
  });

  it('sizes chips proportionally to their count', () => {
    renderWithProviders(<TagCloud {...defaultProps()} />);
    const highCountChip = screen.getByText('fantasy (100)').closest('.MuiChip-root');
    const lowCountChip = screen.getByText('scifi (5)').closest('.MuiChip-root');
    expect(highCountChip).not.toBeNull();
    expect(lowCountChip).not.toBeNull();

    const highFontSize = parseFloat(getComputedStyle(highCountChip as HTMLElement).fontSize);
    const lowFontSize = parseFloat(getComputedStyle(lowCountChip as HTMLElement).fontSize);
    expect(highFontSize).toBeGreaterThan(lowFontSize);
  });

  it('calls onTagsChange with the tag added when an unselected tag is clicked', () => {
    const onTagsChange = vi.fn();
    renderWithProviders(<TagCloud {...defaultProps({ onTagsChange })} />);

    fireEvent.click(screen.getByText('fantasy (100)'));

    expect(onTagsChange).toHaveBeenCalledWith(['fantasy']);
  });

  it('calls onTagsChange with the tag removed when a selected tag is clicked', () => {
    const onTagsChange = vi.fn();
    renderWithProviders(
      <TagCloud {...defaultProps({ selectedTags: ['fantasy', 'mystery'], onTagsChange })} />
    );

    fireEvent.click(screen.getByText('fantasy (100)'));

    expect(onTagsChange).toHaveBeenCalledWith(['mystery']);
  });

  it('renders selected tags in the filled/primary style', () => {
    renderWithProviders(<TagCloud {...defaultProps({ selectedTags: ['fantasy'] })} />);
    const selectedChip = screen.getByText('fantasy (100)').closest('.MuiChip-root');
    expect(selectedChip).toHaveClass('MuiChip-filled');
    expect(selectedChip).toHaveClass('MuiChip-colorPrimary');
  });

  it('toggles collapse when the header is clicked', () => {
    renderWithProviders(<TagCloud {...defaultProps()} />);
    expect(screen.getByLabelText('Collapse tag cloud')).toBeInTheDocument();

    fireEvent.click(screen.getByText('Browse by Tag'));

    expect(screen.getByLabelText('Expand tag cloud')).toBeInTheDocument();
  });
});
