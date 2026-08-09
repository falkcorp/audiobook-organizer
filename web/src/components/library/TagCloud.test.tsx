// file: web/src/components/library/TagCloud.test.tsx
// version: 1.1.0
// guid: 4f3d2c1b-8a9e-4d7c-b6a5-2e1f9c8d7b6a
// last-edited: 2026-08-08

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
  // The panel persists its open/closed state, so tests would otherwise leak
  // into each other depending on execution order.
  localStorage.clear();
});

/** Enough tags to exceed PREVIEW_COUNT (5), ordered so sorting is observable. */
function manyTags() {
  return [
    { tag: 'alpha', count: 1 },
    { tag: 'bravo', count: 90 },
    { tag: 'charlie', count: 3 },
    { tag: 'delta', count: 80 },
    { tag: 'echo', count: 70 },
    { tag: 'foxtrot', count: 60 },
    { tag: 'golf', count: 50 },
  ];
}

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

  it('starts collapsed, so the book grid is not pushed below the fold', () => {
    renderWithProviders(<TagCloud {...defaultProps()} />);
    expect(screen.getByLabelText('Expand tag cloud')).toBeInTheDocument();
  });

  it('toggles open when the header is clicked', () => {
    renderWithProviders(<TagCloud {...defaultProps()} />);
    fireEvent.click(screen.getByText(/Browse by Tag/));
    expect(screen.getByLabelText('Collapse tag cloud')).toBeInTheDocument();
  });

  it('shows only the top 5 tags by count while collapsed', () => {
    renderWithProviders(<TagCloud {...defaultProps({ availableTags: manyTags() })} />);

    // The five busiest, regardless of the order they were passed in.
    for (const label of ['bravo (90)', 'delta (80)', 'echo (70)', 'foxtrot (60)', 'golf (50)']) {
      expect(screen.getByText(label)).toBeInTheDocument();
    }
    // The long tail stays hidden until asked for.
    expect(screen.queryByText('charlie (3)')).not.toBeInTheDocument();
    expect(screen.queryByText('alpha (1)')).not.toBeInTheDocument();
  });

  it('reveals the full cloud via "Show all"', () => {
    renderWithProviders(<TagCloud {...defaultProps({ availableTags: manyTags() })} />);
    fireEvent.click(screen.getByRole('button', { name: /Show all 7/ }));
    expect(screen.getByText('alpha (1)')).toBeInTheDocument();
    expect(screen.getByText('charlie (3)')).toBeInTheDocument();
  });

  // A selected tag outside the preview must stay visible, or the user is left
  // looking at a filtered list with no visible control to clear it.
  it('always shows a selected tag even when it falls outside the top 5', () => {
    renderWithProviders(
      <TagCloud {...defaultProps({ availableTags: manyTags(), selectedTags: ['alpha'] })} />
    );
    expect(screen.getByText('alpha (1)')).toBeInTheDocument();
  });

  it('renders each tag once while collapsed', () => {
    renderWithProviders(<TagCloud {...defaultProps()} />);
    // Regression guard: Collapse keeps children mounted by default, which
    // rendered every chip twice (preview + hidden list) until unmountOnExit.
    expect(screen.getAllByText('fantasy (100)')).toHaveLength(1);
  });

  it('remembers that the cloud was opened', () => {
    const { unmount } = renderWithProviders(<TagCloud {...defaultProps()} />);
    fireEvent.click(screen.getByText(/Browse by Tag/));
    expect(screen.getByLabelText('Collapse tag cloud')).toBeInTheDocument();
    unmount();

    renderWithProviders(<TagCloud {...defaultProps()} />);
    expect(screen.getByLabelText('Collapse tag cloud')).toBeInTheDocument();
  });
});
