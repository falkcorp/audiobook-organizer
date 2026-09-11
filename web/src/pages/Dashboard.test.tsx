// file: web/src/pages/Dashboard.test.tsx
// version: 1.1.0
// last-edited: 2026-09-11

import { describe, it, expect, vi, beforeEach } from 'vitest';
import { screen, waitFor, fireEvent } from '@testing-library/react';
import { renderWithProviders } from '../test/renderWithProviders';
import { Dashboard } from './Dashboard';

// Mock the API module
vi.mock('../services/api', () => ({
  getSystemStatus: vi.fn(),
  getSystemStorage: vi.fn(),
  countAuthors: vi.fn(),
  countSeries: vi.fn(),
  countBooksFiltered: vi.fn(),
  startScan: vi.fn(),
  startOrganize: vi.fn(),
}));

// Mock the operations store
vi.mock('../stores/useOperationsStore', () => ({
  useOperationsStore: Object.assign(
    vi.fn(() => ({})),
    {
      getState: () => ({ startPolling: vi.fn() }),
    }
  ),
}));

// Mock AnnouncementBanner to avoid its own fetch calls
vi.mock('../components/AnnouncementBanner', () => ({
  AnnouncementBanner: () => null,
}));

import {
  getSystemStatus,
  getSystemStorage,
  countAuthors,
  countSeries,
  countBooksFiltered,
} from '../services/api';

const mockGetSystemStatus = vi.mocked(getSystemStatus);
const mockGetSystemStorage = vi.mocked(getSystemStorage);
const mockCountAuthors = vi.mocked(countAuthors);
const mockCountSeries = vi.mocked(countSeries);
const mockCountBooksFiltered = vi.mocked(countBooksFiltered);

const mockMemory = {
  alloc_bytes: 0,
  total_alloc_bytes: 0,
  sys_bytes: 0,
  num_gc: 0,
  heap_alloc: 0,
  heap_sys: 0,
};
const mockRuntime = {
  go_version: '1.24',
  num_goroutine: 10,
  num_cpu: 8,
  os: 'linux',
  arch: 'amd64',
};

function mockSuccessfulAPIs() {
  mockGetSystemStatus.mockResolvedValue({
    status: 'ok',
    library_book_count: 500,
    import_book_count: 25,
    total_book_count: 525,
    total_file_count: 600,
    author_count: 120,
    series_count: 80,
    library_size_bytes: 50 * 1024 * 1024 * 1024, // 50 GB
    import_size_bytes: 2 * 1024 * 1024 * 1024, // 2 GB
    total_size_bytes: 52 * 1024 * 1024 * 1024,
    disk_total_bytes: 500 * 1024 * 1024 * 1024,
    disk_used_bytes: 52 * 1024 * 1024 * 1024,
    library: { book_count: 500, folder_count: 1, total_size: 50 * 1024 * 1024 * 1024 },
    import_paths: { book_count: 25, folder_count: 2, total_size: 2 * 1024 * 1024 * 1024 },
    memory: mockMemory,
    runtime: mockRuntime,
    operations: { recent: [] },
  });
  mockGetSystemStorage.mockResolvedValue({
    path: '/',
    total_bytes: 0,
    used_bytes: 0,
    free_bytes: 0,
    percent_used: 0,
    quota_enabled: false,
    quota_percent: 0,
    user_quotas_enabled: false,
  });
  mockCountAuthors.mockResolvedValue(120);
  mockCountSeries.mockResolvedValue(80);
  mockCountBooksFiltered.mockResolvedValue(25);
}

beforeEach(() => {
  vi.clearAllMocks();
});

describe('Dashboard', () => {
  describe('loading state', () => {
    it('shows skeleton loaders before data arrives', () => {
      // Never resolve the promises — keeps the component in loading state
      mockGetSystemStatus.mockReturnValue(new Promise(() => {}));
      mockCountAuthors.mockReturnValue(new Promise(() => {}));
      mockCountSeries.mockReturnValue(new Promise(() => {}));
      mockCountBooksFiltered.mockReturnValue(new Promise(() => {}));

      renderWithProviders(<Dashboard />);
      expect(screen.getByText('Dashboard')).toBeInTheDocument();
      // StatCard titles are visible even while loading
      expect(screen.getByText('Library Books')).toBeInTheDocument();
      expect(screen.getByText('Authors')).toBeInTheDocument();
    });
  });

  describe('populated state', () => {
    beforeEach(() => {
      mockSuccessfulAPIs();
    });

    it('renders library book count', async () => {
      renderWithProviders(<Dashboard />);
      await waitFor(() => {
        expect(screen.getByText('500')).toBeInTheDocument();
      });
    });

    it('renders import path book count', async () => {
      renderWithProviders(<Dashboard />);
      await waitFor(() => {
        expect(screen.getByText('Import Path Books')).toBeInTheDocument();
        // "25" appears in multiple cards (import books + needs organizing),
        // so we verify the Import Path Books card title is present
        expect(screen.getAllByText('25').length).toBeGreaterThanOrEqual(1);
      });
    });

    it('renders author count', async () => {
      renderWithProviders(<Dashboard />);
      await waitFor(() => {
        expect(screen.getByText('120')).toBeInTheDocument();
      });
    });

    it('renders series count', async () => {
      renderWithProviders(<Dashboard />);
      await waitFor(() => {
        expect(screen.getByText('80')).toBeInTheDocument();
      });
    });

    it('renders storage usage section', async () => {
      renderWithProviders(<Dashboard />);
      await waitFor(() => {
        expect(screen.getByText('Storage Usage')).toBeInTheDocument();
        expect(screen.getByText(/Total Size/)).toBeInTheDocument();
      });
    });

    it('renders recent operations section', async () => {
      renderWithProviders(<Dashboard />);
      await waitFor(() => {
        expect(screen.getByText('Recent Operations')).toBeInTheDocument();
        expect(screen.getByText('No recent operations')).toBeInTheDocument();
      });
    });

    it('renders quick actions', async () => {
      renderWithProviders(<Dashboard />);
      await waitFor(() => {
        expect(screen.getByText('Quick Actions')).toBeInTheDocument();
        expect(screen.getByRole('button', { name: /Scan All Import Paths/ })).toBeInTheDocument();
        expect(screen.getByRole('button', { name: /Organize All/ })).toBeInTheDocument();
      });
    });
  });

  describe('with recent operations', () => {
    it('renders operation entries', async () => {
      mockGetSystemStatus.mockResolvedValue({
        status: 'ok',
        library: { book_count: 10, folder_count: 1, total_size: 0 },
        import_paths: { book_count: 0, folder_count: 0, total_size: 0 },
        memory: mockMemory,
        runtime: mockRuntime,
        operations: {
          recent: [
            {
              id: 'op-1',
              type: 'scan',
              status: 'completed',
              progress: 50,
              total: 50,
              message: 'Scanned 50 books',
              created_at: '2026-04-17T10:00:00Z',
            },
            {
              id: 'op-2',
              type: 'organize',
              status: 'failed',
              progress: 0,
              total: 0,
              message: 'Organization failed',
              created_at: '2026-04-17T09:00:00Z',
            },
          ],
        },
      });
      mockGetSystemStorage.mockResolvedValue({
        path: '/',
        total_bytes: 0,
        used_bytes: 0,
        free_bytes: 0,
        percent_used: 0,
        quota_enabled: false,
        quota_percent: 0,
        user_quotas_enabled: false,
      });
      mockCountAuthors.mockResolvedValue(5);
      mockCountSeries.mockResolvedValue(3);
      mockCountBooksFiltered.mockResolvedValue(0);

      renderWithProviders(<Dashboard />);
      await waitFor(() => {
        expect(screen.getByText('Scanned 50 books')).toBeInTheDocument();
        expect(screen.getByText('Organization failed')).toBeInTheDocument();
      });
    });
  });

  // A failed request must render as a FAILURE, not as an empty library. Until
  // 2026-09-11 every catch wrote 0 into its tile, so a backend blip showed
  // "0 authors, 0 series, 0 imported" with no indication anything was wrong
  // (WEB-03). These pin the four states apart: no fabricated zeros, a visible
  // error, and a Retry that re-fires only what failed.
  describe('error state', () => {
    it('renders the error state and no zeros when every request fails', async () => {
      mockGetSystemStatus.mockRejectedValue(new Error('status down'));
      mockGetSystemStorage.mockRejectedValue(new Error('storage down'));
      mockCountAuthors.mockRejectedValue(new Error('authors down'));
      mockCountSeries.mockRejectedValue(new Error('series down'));
      mockCountBooksFiltered.mockRejectedValue(new Error('imported down'));

      renderWithProviders(<Dashboard />);

      const banner = await screen.findByTestId('dashboard-load-error');
      expect(banner).toHaveTextContent('System status: status down');
      expect(banner).toHaveTextContent('Author count: authors down');
      expect(banner).toHaveTextContent('Series count: series down');
      expect(banner).toHaveTextContent('Books awaiting organization: imported down');

      // Library Books, Import Path Books, Authors, Series, Needs Organizing.
      expect(screen.getAllByTestId(/^stat-unavailable-/)).toHaveLength(5);
      expect(screen.getAllByText('Count unavailable')).toHaveLength(5);
      // The whole point: nothing on the page claims a count of zero.
      expect(screen.queryByText('0')).not.toBeInTheDocument();
      expect(screen.queryByText('All Books Organized')).not.toBeInTheDocument();

      // The two panels fed by system status say so too, rather than drawing
      // "0.0 GB / 0.0 GB" and "No recent operations".
      expect(screen.getByTestId('storage-error')).toHaveTextContent('status down');
      expect(screen.getByTestId('recent-operations-error')).toHaveTextContent('status down');
      expect(screen.queryByText('No recent operations')).not.toBeInTheDocument();
      expect(screen.queryByText(/GB \//)).not.toBeInTheDocument();
    });

    it('Retry re-fires only the loads that failed', async () => {
      mockSuccessfulAPIs();
      mockCountAuthors.mockRejectedValueOnce(new Error('authors down')).mockResolvedValue(120);

      renderWithProviders(<Dashboard />);

      const banner = await screen.findByTestId('dashboard-load-error');
      expect(banner).toHaveTextContent('Author count: authors down');
      expect(mockCountAuthors).toHaveBeenCalledTimes(1);
      expect(mockCountSeries).toHaveBeenCalledTimes(1);

      fireEvent.click(screen.getByRole('button', { name: 'Retry' }));

      await waitFor(() => {
        expect(screen.queryByTestId('dashboard-load-error')).not.toBeInTheDocument();
      });
      expect(screen.getByText('120')).toBeInTheDocument();
      expect(mockCountAuthors).toHaveBeenCalledTimes(2);
      // The series count loaded fine the first time and is not re-requested.
      expect(mockCountSeries).toHaveBeenCalledTimes(1);
    });

    it('keeps a known value and marks it stale when only its refresh fails', async () => {
      // countAuthors fails, but system status delivered author_count = 120.
      // The tile shows 120 with a stale marker, not "—" and not 0.
      mockSuccessfulAPIs();
      mockCountAuthors.mockRejectedValue(new Error('authors down'));

      renderWithProviders(<Dashboard />);

      await screen.findByTestId('dashboard-load-error');
      expect(screen.getByText('120')).toBeInTheDocument();
      expect(screen.getByTestId('stat-stale-Authors')).toBeInTheDocument();
      expect(screen.queryByTestId('stat-unavailable-Authors')).not.toBeInTheDocument();
      // The sibling that succeeded is untouched.
      expect(screen.getByText('80')).toBeInTheDocument();
      expect(screen.queryByTestId('stat-stale-Series')).not.toBeInTheDocument();
    });
  });

  describe('needs organizing card', () => {
    it('shows "All Books Organized" when import count is 0', async () => {
      mockSuccessfulAPIs();
      mockCountBooksFiltered.mockResolvedValue(0);

      renderWithProviders(<Dashboard />);
      await waitFor(() => {
        expect(screen.getByText('All Books Organized')).toBeInTheDocument();
      });
    });

    it('shows "Needs Organizing" when import count > 0', async () => {
      mockSuccessfulAPIs();
      mockCountBooksFiltered.mockResolvedValue(42);

      renderWithProviders(<Dashboard />);
      await waitFor(() => {
        expect(screen.getByText('Needs Organizing')).toBeInTheDocument();
        expect(screen.getByText('42')).toBeInTheDocument();
      });
    });
  });
});
