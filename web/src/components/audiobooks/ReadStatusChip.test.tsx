// file: web/src/components/audiobooks/ReadStatusChip.test.tsx
// version: 1.0.1

import { describe, it, expect, vi, beforeEach } from 'vitest';
import { screen, fireEvent, waitFor } from '@testing-library/react';
import { renderWithProviders } from '../../test/renderWithProviders';
import { buildBookState } from '../../test/factories';
import ReadStatusChip from './ReadStatusChip';

// Mock the entire readingApi module
vi.mock('../../services/readingApi', () => ({
  READ_STATUS_LABELS: {
    unstarted: 'Unstarted',
    in_progress: 'In Progress',
    finished: 'Finished',
    abandoned: 'Abandoned',
  },
  READ_STATUS_COLORS: {
    unstarted: '#9e9e9e',
    in_progress: '#2196f3',
    finished: '#4caf50',
    abandoned: '#ff9800',
  },
  getBookState: vi.fn(),
  setBookStatus: vi.fn(),
  clearBookStatus: vi.fn(),
}));

import { getBookState, setBookStatus, clearBookStatus } from '../../services/readingApi';

const mockGetBookState = vi.mocked(getBookState);
const mockSetBookStatus = vi.mocked(setBookStatus);
const mockClearBookStatus = vi.mocked(clearBookStatus);

beforeEach(() => {
  vi.clearAllMocks();
});

describe('ReadStatusChip', () => {
  describe('rendering', () => {
    it('shows "Unstarted" when book has no state', async () => {
      mockGetBookState.mockResolvedValue(null);
      renderWithProviders(<ReadStatusChip bookId="book-1" />);
      await waitFor(() => {
        expect(screen.getByText('Unstarted')).toBeInTheDocument();
      });
    });

    it('shows "Finished" for a finished book', async () => {
      mockGetBookState.mockResolvedValue(buildBookState({ book_id: 'book-1', status: 'finished' }));
      renderWithProviders(<ReadStatusChip bookId="book-1" />);
      await waitFor(() => {
        expect(screen.getByText('Finished')).toBeInTheDocument();
      });
    });

    it('shows progress percentage for in_progress books', async () => {
      mockGetBookState.mockResolvedValue(
        buildBookState({ book_id: 'book-1', status: 'in_progress', progress_pct: 42 })
      );
      renderWithProviders(<ReadStatusChip bookId="book-1" />);
      await waitFor(() => {
        expect(screen.getByText('42%')).toBeInTheDocument();
      });
    });

    it('shows a progress bar for in_progress books', async () => {
      mockGetBookState.mockResolvedValue(
        buildBookState({ book_id: 'book-1', status: 'in_progress', progress_pct: 75 })
      );
      renderWithProviders(<ReadStatusChip bookId="book-1" />);
      await waitFor(() => {
        expect(screen.getByRole('progressbar')).toBeInTheDocument();
      });
    });
  });

  describe('compact mode', () => {
    it('hides the label text in compact mode', async () => {
      mockGetBookState.mockResolvedValue(buildBookState({ book_id: 'book-1', status: 'finished' }));
      renderWithProviders(<ReadStatusChip bookId="book-1" compact />);
      await waitFor(() => {
        // The chip should be rendered but without label text
        expect(screen.queryByText('Finished')).not.toBeInTheDocument();
      });
    });

    it('hides progress bar in compact mode', async () => {
      mockGetBookState.mockResolvedValue(
        buildBookState({ book_id: 'book-1', status: 'in_progress', progress_pct: 50 })
      );
      renderWithProviders(<ReadStatusChip bookId="book-1" compact />);
      await waitFor(() => {
        expect(screen.queryByRole('progressbar')).not.toBeInTheDocument();
      });
    });
  });

  describe('status menu', () => {
    it('opens a menu when the chip is clicked', async () => {
      mockGetBookState.mockResolvedValue(null);
      renderWithProviders(<ReadStatusChip bookId="book-1" />);
      await waitFor(() => {
        expect(screen.getByText('Unstarted')).toBeInTheDocument();
      });
      // Click the chip to open the menu
      fireEvent.click(screen.getByText('Unstarted'));
      expect(screen.getByRole('menu')).toBeInTheDocument();
      // All 4 statuses should appear as menu items
      expect(screen.getAllByRole('menuitem').length).toBeGreaterThanOrEqual(4);
    });

    it('calls setBookStatus when a status is selected', async () => {
      mockGetBookState.mockResolvedValue(null);
      const updatedState = buildBookState({
        book_id: 'book-1',
        status: 'finished',
      });
      mockSetBookStatus.mockResolvedValue(updatedState);

      renderWithProviders(<ReadStatusChip bookId="book-1" />);
      await waitFor(() => {
        expect(screen.getByText('Unstarted')).toBeInTheDocument();
      });

      fireEvent.click(screen.getByText('Unstarted'));
      // Find "Finished" in the menu items (not the chip)
      const menuItems = screen.getAllByRole('menuitem');
      const finishedItem = menuItems.find((item) => item.textContent?.includes('Finished'));
      expect(finishedItem).toBeDefined();
      fireEvent.click(finishedItem!);

      await waitFor(() => {
        expect(mockSetBookStatus).toHaveBeenCalledWith('book-1', 'finished');
      });
    });

    it('shows "Reset to auto-detected" when status_manual is true', async () => {
      mockGetBookState.mockResolvedValue(
        buildBookState({ book_id: 'book-1', status: 'finished', status_manual: true })
      );
      renderWithProviders(<ReadStatusChip bookId="book-1" />);
      await waitFor(() => {
        expect(screen.getByText('Finished')).toBeInTheDocument();
      });

      fireEvent.click(screen.getByText('Finished'));
      expect(screen.getByText('Reset to auto-detected')).toBeInTheDocument();
    });

    it('calls clearBookStatus when reset is clicked', async () => {
      mockGetBookState.mockResolvedValue(
        buildBookState({ book_id: 'book-1', status: 'finished', status_manual: true })
      );
      mockClearBookStatus.mockResolvedValue(
        buildBookState({ book_id: 'book-1', status: 'unstarted', status_manual: false })
      );

      renderWithProviders(<ReadStatusChip bookId="book-1" />);
      await waitFor(() => {
        expect(screen.getByText('Finished')).toBeInTheDocument();
      });

      fireEvent.click(screen.getByText('Finished'));
      fireEvent.click(screen.getByText('Reset to auto-detected'));

      await waitFor(() => {
        expect(mockClearBookStatus).toHaveBeenCalledWith('book-1');
      });
    });
  });
});
