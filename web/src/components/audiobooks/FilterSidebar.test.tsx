// file: web/src/components/audiobooks/FilterSidebar.test.tsx
// version: 1.0.1

import { describe, it, expect, vi, beforeEach } from 'vitest';
import { screen, fireEvent, within } from '@testing-library/react';
import { renderWithProviders } from '../../test/renderWithProviders';
import { FilterSidebar } from './FilterSidebar';
import type { FilterOptions } from '../../types/index';

function defaultProps(overrides: Partial<Parameters<typeof FilterSidebar>[0]> = {}) {
  return {
    open: true,
    onClose: vi.fn(),
    filters: {} as FilterOptions,
    onFiltersChange: vi.fn(),
    authors: ['Brandon Sanderson', 'Joe Abercrombie'],
    series: ['Stormlight Archive', 'First Law'],
    genres: ['Fantasy', 'Science Fiction'],
    languages: ['English', 'Spanish'],
    ...overrides,
  };
}

beforeEach(() => {
  vi.clearAllMocks();
});

describe('FilterSidebar', () => {
  describe('rendering', () => {
    it('renders the Filters heading', () => {
      renderWithProviders(<FilterSidebar {...defaultProps()} />);
      expect(screen.getByText('Filters')).toBeInTheDocument();
    });

    it('renders filter dropdowns for all categories', () => {
      renderWithProviders(<FilterSidebar {...defaultProps()} />);
      expect(screen.getByLabelText('Library State')).toBeInTheDocument();
      expect(screen.getByLabelText('Author')).toBeInTheDocument();
      expect(screen.getByLabelText('Series')).toBeInTheDocument();
      expect(screen.getByLabelText('Genre')).toBeInTheDocument();
      expect(screen.getByLabelText('Language')).toBeInTheDocument();
    });

    it('renders Clear All button', () => {
      renderWithProviders(<FilterSidebar {...defaultProps()} />);
      expect(screen.getByRole('button', { name: 'Clear All' })).toBeInTheDocument();
    });
  });

  describe('filter selection', () => {
    it('calls onFiltersChange when author is selected', () => {
      const onFiltersChange = vi.fn();
      renderWithProviders(<FilterSidebar {...defaultProps({ onFiltersChange })} />);

      // Open the Author select
      const authorSelect = screen.getByLabelText('Author');
      fireEvent.mouseDown(authorSelect);

      // Select an author from the listbox
      const listbox = within(screen.getByRole('listbox'));
      fireEvent.click(listbox.getByText('Brandon Sanderson'));

      expect(onFiltersChange).toHaveBeenCalledWith(
        expect.objectContaining({ author: 'Brandon Sanderson' })
      );
    });

    it('calls onFiltersChange when genre is selected', () => {
      const onFiltersChange = vi.fn();
      renderWithProviders(<FilterSidebar {...defaultProps({ onFiltersChange })} />);

      fireEvent.mouseDown(screen.getByLabelText('Genre'));
      const listbox = within(screen.getByRole('listbox'));
      fireEvent.click(listbox.getByText('Fantasy'));

      expect(onFiltersChange).toHaveBeenCalledWith(expect.objectContaining({ genre: 'Fantasy' }));
    });

    it('calls onFiltersChange when library state is selected', () => {
      const onFiltersChange = vi.fn();
      renderWithProviders(<FilterSidebar {...defaultProps({ onFiltersChange })} />);

      fireEvent.mouseDown(screen.getByLabelText('Library State'));
      const listbox = within(screen.getByRole('listbox'));
      fireEvent.click(listbox.getByText('Organized'));

      expect(onFiltersChange).toHaveBeenCalledWith(
        expect.objectContaining({ libraryState: 'organized' })
      );
    });
  });

  describe('clear all', () => {
    it('resets all filters when Clear All is clicked', () => {
      const onFiltersChange = vi.fn();
      const onTagsChange = vi.fn();
      renderWithProviders(
        <FilterSidebar
          {...defaultProps({
            filters: { author: 'Brandon Sanderson', genre: 'Fantasy' },
            onFiltersChange,
            onTagsChange,
            selectedTags: ['favorite'],
          })}
        />
      );

      fireEvent.click(screen.getByRole('button', { name: 'Clear All' }));
      expect(onFiltersChange).toHaveBeenCalledWith({});
      expect(onTagsChange).toHaveBeenCalledWith([]);
    });
  });

  describe('active filter count', () => {
    it('shows count chip when filters are active', () => {
      renderWithProviders(
        <FilterSidebar
          {...defaultProps({
            filters: { author: 'Brandon Sanderson', genre: 'Fantasy' },
          })}
        />
      );
      // The active filter count chip should show "2"
      expect(screen.getByText('2')).toBeInTheDocument();
    });

    it('does not show count chip when no filters are active', () => {
      renderWithProviders(<FilterSidebar {...defaultProps({ filters: {} })} />);
      // No count chip should be present (there's no "0" chip)
      expect(screen.queryByText('0')).not.toBeInTheDocument();
    });
  });

  describe('tags autocomplete', () => {
    it('renders tags section when onTagsChange is provided', () => {
      renderWithProviders(
        <FilterSidebar
          {...defaultProps({
            onTagsChange: vi.fn(),
            availableTags: [
              { tag: 'favorite', count: 5 },
              { tag: 'to-read', count: 3 },
            ],
          })}
        />
      );
      expect(screen.getByText('Tags')).toBeInTheDocument();
    });

    it('does not render tags section when onTagsChange is absent', () => {
      renderWithProviders(<FilterSidebar {...defaultProps({ onTagsChange: undefined })} />);
      expect(screen.queryByText('Tags')).not.toBeInTheDocument();
    });

    it('shows intersection hint when tags are selected', () => {
      renderWithProviders(
        <FilterSidebar
          {...defaultProps({
            onTagsChange: vi.fn(),
            selectedTags: ['favorite'],
            availableTags: [{ tag: 'favorite', count: 5 }],
          })}
        />
      );
      expect(screen.getByText('Books must have ALL selected tags')).toBeInTheDocument();
    });
  });

  describe('closed state', () => {
    it('does not render content when closed', () => {
      renderWithProviders(<FilterSidebar {...defaultProps({ open: false })} />);
      // MUI Drawer hides content when closed
      expect(screen.queryByText('Filters')).not.toBeInTheDocument();
    });
  });
});
