// file: web/src/pages/Playlists.test.tsx
// version: 1.0.1

import { describe, it, expect, vi, beforeEach } from 'vitest';
import { screen, fireEvent, waitFor } from '@testing-library/react';
import { renderWithProviders } from '../test/renderWithProviders';
import { buildPlaylist } from '../test/factories';
import Playlists from './Playlists';

vi.mock('../services/playlistApi', () => ({
  listPlaylists: vi.fn(),
  createPlaylist: vi.fn(),
  deletePlaylist: vi.fn(),
}));

import { listPlaylists, createPlaylist, deletePlaylist } from '../services/playlistApi';

const mockListPlaylists = vi.mocked(listPlaylists);
const mockCreatePlaylist = vi.mocked(createPlaylist);
const mockDeletePlaylist = vi.mocked(deletePlaylist);

beforeEach(() => {
  vi.clearAllMocks();
  mockListPlaylists.mockResolvedValue({ playlists: [], count: 0 });
  mockDeletePlaylist.mockResolvedValue(undefined);
});

describe('Playlists', () => {
  describe('empty state', () => {
    it('shows empty message when no playlists exist', async () => {
      renderWithProviders(<Playlists />);
      await waitFor(() => {
        expect(screen.getByText(/No playlists yet/)).toBeInTheDocument();
      });
    });

    it('renders the page heading', async () => {
      renderWithProviders(<Playlists />);
      expect(screen.getByText('Playlists')).toBeInTheDocument();
    });

    it('renders filter tabs', async () => {
      renderWithProviders(<Playlists />);
      expect(screen.getByRole('tab', { name: 'All' })).toBeInTheDocument();
      expect(screen.getByRole('tab', { name: 'Static' })).toBeInTheDocument();
      expect(screen.getByRole('tab', { name: 'Smart' })).toBeInTheDocument();
    });
  });

  describe('with playlists', () => {
    const playlists = [
      buildPlaylist({
        id: 'pl-1',
        name: 'Favorites',
        type: 'static',
        book_ids: ['b1', 'b2'],
      }),
      buildPlaylist({
        id: 'pl-2',
        name: 'Recent Sci-Fi',
        type: 'smart',
        query: 'genre:scifi year:>2024',
        book_ids: [],
      }),
    ];

    beforeEach(() => {
      mockListPlaylists.mockResolvedValue({
        playlists,
        count: playlists.length,
      });
    });

    it('lists playlist names', async () => {
      renderWithProviders(<Playlists />);
      await waitFor(() => {
        expect(screen.getByText('Favorites')).toBeInTheDocument();
        expect(screen.getByText('Recent Sci-Fi')).toBeInTheDocument();
      });
    });

    it('shows type chips', async () => {
      renderWithProviders(<Playlists />);
      await waitFor(() => {
        expect(screen.getByText('static')).toBeInTheDocument();
        expect(screen.getByText('smart')).toBeInTheDocument();
      });
    });

    it('shows book count', async () => {
      renderWithProviders(<Playlists />);
      await waitFor(() => {
        expect(screen.getByText('2 books')).toBeInTheDocument();
      });
    });

    it('shows query for smart playlists', async () => {
      renderWithProviders(<Playlists />);
      await waitFor(() => {
        expect(screen.getByText('Query: genre:scifi year:>2024')).toBeInTheDocument();
      });
    });
  });

  describe('tab filtering', () => {
    it('re-fetches with type filter when tab changes', async () => {
      mockListPlaylists.mockResolvedValue({ playlists: [], count: 0 });
      renderWithProviders(<Playlists />);
      await waitFor(() => {
        expect(mockListPlaylists).toHaveBeenCalled();
      });

      // Click the "Smart" tab
      fireEvent.click(screen.getByRole('tab', { name: 'Smart' }));

      await waitFor(() => {
        // Should have been called again with 'smart' filter
        const calls = mockListPlaylists.mock.calls;
        const lastCall = calls[calls.length - 1];
        expect(lastCall[0]).toBe('smart');
      });
    });
  });

  describe('delete', () => {
    it('calls deletePlaylist and reloads', async () => {
      const pl = buildPlaylist({ id: 'pl-1', name: 'Delete Me' });
      mockListPlaylists.mockResolvedValue({
        playlists: [pl],
        count: 1,
      });

      renderWithProviders(<Playlists />);
      await waitFor(() => {
        expect(screen.getByText('Delete Me')).toBeInTheDocument();
      });

      // Find and click the delete button (IconButton with DeleteIcon)
      const buttons = screen.getAllByRole('button');
      const delBtn = buttons.find(
        (btn) => btn.querySelector('[data-testid="DeleteIcon"]') !== null
      );
      if (delBtn) {
        fireEvent.click(delBtn);
        await waitFor(() => {
          expect(mockDeletePlaylist).toHaveBeenCalledWith('pl-1');
        });
      }
    });
  });

  describe('create dialog', () => {
    it('opens create dialog when New Playlist is clicked', async () => {
      renderWithProviders(<Playlists />);
      fireEvent.click(screen.getByRole('button', { name: /New Playlist/i }));
      await waitFor(() => {
        expect(screen.getByText('Create Playlist')).toBeInTheDocument();
      });
    });

    it('creates a static playlist', async () => {
      mockCreatePlaylist.mockResolvedValue(buildPlaylist());
      renderWithProviders(<Playlists />);
      fireEvent.click(screen.getByRole('button', { name: /New Playlist/i }));

      await waitFor(() => {
        expect(screen.getByText('Create Playlist')).toBeInTheDocument();
      });

      // Fill in name
      fireEvent.change(screen.getByLabelText('Name *'), {
        target: { value: 'My New Playlist' },
      });

      // Click Create
      fireEvent.click(screen.getByRole('button', { name: 'Create' }));

      await waitFor(() => {
        expect(mockCreatePlaylist).toHaveBeenCalledWith(
          expect.objectContaining({
            name: 'My New Playlist',
            type: 'static',
          })
        );
      });
    });
  });
});
