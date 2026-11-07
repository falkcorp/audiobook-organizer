// file: web/tests/unit/BookDetail.test.tsx
// version: 1.3.2
// guid: 1a2b3c4d-5e6f-7a8b-9c0d-1e2f3a4b5c6d
// last-edited: 2026-11-07

/**
 * Unit tests for BookDetail component
 * Tests loading states, button behavior, and state management
 */

import { describe, it, expect, vi, beforeEach } from 'vitest';
import { render, screen, waitFor } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { BrowserRouter } from 'react-router';
import { BookDetail } from '../../src/pages/BookDetail';
import * as api from '../../src/services/api';

// Mock the API module
vi.mock('../../src/services/api', async () => {
  const actual = await vi.importActual<typeof import('../../src/services/api')>(
    '../../src/services/api'
  );
  return {
    ...actual,
    getBook: vi.fn(),
    getBookVersions: vi.fn(),
    getBookTags: vi.fn(),
    getBookSegments: vi.fn(),
    fetchBookMetadata: vi.fn(),
    parseAudiobookWithAI: vi.fn(),
    updateBook: vi.fn(),
    deleteBook: vi.fn(),
    restoreSoftDeletedBook: vi.fn(),
  };
});

const navigateMock = vi.fn();

// Mock react-router hooks
vi.mock('react-router', async () => {
  const actual = await vi.importActual('react-router');
  return {
    ...actual,
    useParams: () => ({ id: 'test-book-id' }),
    useNavigate: () => navigateMock,
  };
});

const mockBook = {
  id: 'test-book-id',
  title: 'The Odyssey',
  author: 'Homer',
  file_path: '/library/test.m4b',
  created_at: '2024-01-01T00:00:00Z',
  updated_at: '2024-01-01T00:00:00Z',
};

const mockTags = {
  media_info: {
    codec: 'aac',
    bitrate: 128,
    sample_rate: 44100,
    channels: 2,
  },
  tags: {
    title: {
      file_value: 'The Odyssey',
      fetched_value: 'The Odyssey: Homer',
      stored_value: 'The Odyssey',
      override_value: null,
      override_locked: false,
      effective_value: 'The Odyssey',
      effective_source: 'stored',
    },
    author_name: {
      file_value: null,
      fetched_value: 'Homer',
      stored_value: null,
      override_value: null,
      override_locked: false,
      effective_value: null,
      effective_source: null,
    },
    audiobook_release_year: {
      file_value: null,
      fetched_value: 2020,
      stored_value: null,
      override_value: null,
      override_locked: false,
      effective_value: null,
      effective_source: null,
    },
  },
};

describe('BookDetail Component', () => {
  beforeEach(() => {
    vi.clearAllMocks();
    navigateMock.mockReset();
    vi.mocked(api.getBook).mockResolvedValue(mockBook);
    vi.mocked(api.getBookVersions).mockResolvedValue([]);
    vi.mocked(api.getBookTags).mockResolvedValue(mockTags);
    vi.mocked(api.getBookSegments).mockResolvedValue([]);
  });

  it('renders book details correctly', async () => {
    render(
      <BrowserRouter>
        <BookDetail />
      </BrowserRouter>
    );

    const titleHeading = await screen.findByRole('heading', {
      name: 'The Odyssey',
    });
    expect(titleHeading).toBeInTheDocument();
  });

  it('Fetch Metadata button shows loading state', async () => {
    const user = userEvent.setup();

    // Manually-controlled deferred so the loading state is observable
    // for as long as the test needs (no race against a fixed setTimeout).
    let resolveFetch: (v: unknown) => void = () => {};
    vi.mocked(api.fetchBookMetadata).mockImplementation(
      () =>
        new Promise((resolve) => {
          resolveFetch = resolve as (v: unknown) => void;
        })
    );

    render(
      <BrowserRouter>
        <BookDetail />
      </BrowserRouter>
    );

    await screen.findByRole('heading', { name: 'The Odyssey' });

    const fetchButton = screen.getByRole('button', { name: /fetch metadata/i });
    expect(fetchButton).toHaveTextContent('Fetch Metadata');

    await user.click(fetchButton);

    // Loading state must appear and stay until we resolve.
    await waitFor(() => {
      expect(fetchButton).toHaveTextContent('Fetching...');
      expect(fetchButton).toBeDisabled();
    });

    // Now release the mock and confirm the button returns to its normal state.
    resolveFetch({
      message: 'Success',
      source: 'Open Library',
      book: mockBook,
    });

    await waitFor(
      () => {
        const btn = screen.getByRole('button', { name: /fetch metadata/i });
        expect(btn).toHaveTextContent('Fetch Metadata');
        expect(btn).not.toBeDisabled();
      },
      { timeout: 3000 }
    );
  });

  it('Parse with AI button shows loading state', async () => {
    const user = userEvent.setup();

    let resolveParse: (v: unknown) => void = () => {};
    vi.mocked(api.parseAudiobookWithAI).mockImplementation(
      () =>
        new Promise((resolve) => {
          resolveParse = resolve as (v: unknown) => void;
        })
    );

    render(
      <BrowserRouter>
        <BookDetail />
      </BrowserRouter>
    );

    await screen.findByRole('heading', { name: 'The Odyssey' });

    const parseButton = screen.getByRole('button', { name: /parse with ai/i });
    expect(parseButton).toHaveTextContent('Parse with AI');

    await user.click(parseButton);

    await waitFor(() => {
      expect(parseButton).toHaveTextContent('Parsing...');
      expect(parseButton).toBeDisabled();
    });

    resolveParse({ message: 'Success', book: mockBook });

    await waitFor(
      () => {
        expect(parseButton).toHaveTextContent('Parse with AI');
        expect(parseButton).not.toBeDisabled();
      },
      { timeout: 2000 }
    );
  });

  it('navigates to Files & History tab and shows tag comparison', async () => {
    const user = userEvent.setup();

    render(
      <BrowserRouter>
        <BookDetail />
      </BrowserRouter>
    );

    await screen.findByRole('heading', { name: 'The Odyssey' });

    // Navigate to Files & History tab
    const filesTab = await screen.findByRole('tab', { name: /files/i });
    await user.click(filesTab);

    // Should show the tag comparison table (Source header in transposed table)
    await waitFor(() => {
      expect(screen.getByText('Source')).toBeInTheDocument();
    }, { timeout: 2000 });
  });

  it('does not switch tabs after fetching metadata', async () => {
    const user = userEvent.setup();

    vi.mocked(api.fetchBookMetadata).mockResolvedValue({
      message: 'Success',
      source: 'Open Library',
      book: mockBook,
    });

    render(
      <BrowserRouter>
        <BookDetail />
      </BrowserRouter>
    );

    await screen.findByRole('heading', { name: 'The Odyssey' });

    // Click the Info tab to ensure we're on it
    const infoTab = screen.getByRole('tab', { name: /^info$/i });
    await user.click(infoTab);
    await waitFor(() => {
      expect(infoTab).toHaveAttribute('aria-selected', 'true');
    });

    const fetchButton = screen.getByRole('button', { name: /fetch metadata/i });
    await user.click(fetchButton);

    await waitFor(() => {
      expect(api.fetchBookMetadata).toHaveBeenCalled();
    });

    // Should still be on Info tab after fetch
    expect(infoTab).toHaveAttribute('aria-selected', 'true');
  });

  it('renders Info and Files tabs', async () => {
    render(
      <BrowserRouter>
        <BookDetail />
      </BrowserRouter>
    );

    await screen.findByRole('heading', { name: 'The Odyssey' });

    // Should have both tabs
    expect(screen.getByRole('tab', { name: /^info$/i })).toBeInTheDocument();
    expect(screen.getByRole('tab', { name: /files/i })).toBeInTheDocument();
  });
});
