// file: web/src/components/TagComparison.test.tsx
// version: 1.0.0
// guid: 8c4e2f5a-7b1e-4d2f-a8c9-3f2e1b4c5d6a
// last-edited: 2026-08-22

import { describe, it, expect, vi } from 'vitest';
import { screen, waitFor } from '@testing-library/react';
import { renderWithProviders } from '../test/renderWithProviders';
import { TagComparison } from './TagComparison';
import * as api from '../services/api';

// Mock the API to return test data
vi.mock('../services/api', async () => {
  const actual = await vi.importActual<typeof import('../services/api')>('../services/api');
  return {
    ...actual,
    getBookTags: vi.fn(),
  };
});

describe('TagComparison', () => {
  const mockVersions = [
    { id: 'book1', title: 'Version 1', format: 'mp3' },
    { id: 'book2', title: 'Version 2', format: 'aac' },
  ];

  const mockTags = {
    tags: {
      title: { file_value: 'Test Title', stored_value: 'Test Title' },
      author_name: { file_value: 'Test Author', stored_value: 'Test Author' },
    },
  };

  it('renders the metadata table immediately without expand interaction', async () => {
    vi.mocked(api.getBookTags).mockResolvedValueOnce(mockTags);

    renderWithProviders(
      <TagComparison bookId="book1" versions={mockVersions} />
    );

    // Wait for the table to render with the mocked data
    // This verifies that the expanded state has been removed and content is always visible
    await waitFor(() => {
      expect(screen.getByText('File')).toBeInTheDocument();
    });
    expect(screen.getByText('DB')).toBeInTheDocument();

    // Verify no tag-comparison-toggle element exists
    const toggleElement = screen.queryByTestId('tag-comparison-toggle');
    expect(toggleElement).not.toBeInTheDocument();
  });
});
