// file: web/src/components/audiobooks/AddToPlaylistDialog.test.tsx
// version: 1.0.1

import { describe, it, expect, vi, beforeEach } from 'vitest';
import { screen, fireEvent, waitFor } from '@testing-library/react';
import { renderWithProviders } from '../../test/renderWithProviders';
import { buildPlaylist } from '../../test/factories';
import AddToPlaylistDialog from './AddToPlaylistDialog';

vi.mock('../../services/playlistApi', () => ({
  listPlaylists: vi.fn(),
  addBooksToPlaylist: vi.fn(),
  createPlaylist: vi.fn(),
}));

import { listPlaylists, addBooksToPlaylist, createPlaylist } from '../../services/playlistApi';

const mockListPlaylists = vi.mocked(listPlaylists);
const mockAddBooksToPlaylist = vi.mocked(addBooksToPlaylist);
const mockCreatePlaylist = vi.mocked(createPlaylist);

const onClose = vi.fn();

beforeEach(() => {
  vi.clearAllMocks();
  mockListPlaylists.mockResolvedValue({ playlists: [], count: 0 });
  mockAddBooksToPlaylist.mockResolvedValue(buildPlaylist());
  mockCreatePlaylist.mockResolvedValue(buildPlaylist());
});

describe('AddToPlaylistDialog', () => {
  describe('when closed', () => {
    it('does not render dialog content', () => {
      renderWithProviders(<AddToPlaylistDialog open={false} onClose={onClose} bookIds={['b1']} />);
      expect(screen.queryByText(/Add.*to Playlist/)).not.toBeInTheDocument();
    });
  });

  describe('when open with no playlists', () => {
    it('shows empty state message', async () => {
      renderWithProviders(<AddToPlaylistDialog open={true} onClose={onClose} bookIds={['b1']} />);
      await waitFor(() => {
        expect(screen.getByText('No static playlists yet.')).toBeInTheDocument();
      });
    });

    it('shows singular title for one book', async () => {
      renderWithProviders(<AddToPlaylistDialog open={true} onClose={onClose} bookIds={['b1']} />);
      await waitFor(() => {
        expect(screen.getByText('Add Book to Playlist')).toBeInTheDocument();
      });
    });

    it('shows plural title for multiple books', async () => {
      renderWithProviders(
        <AddToPlaylistDialog open={true} onClose={onClose} bookIds={['b1', 'b2', 'b3']} />
      );
      await waitFor(() => {
        expect(screen.getByText('Add 3 Books to Playlist')).toBeInTheDocument();
      });
    });
  });

  describe('when open with existing playlists', () => {
    const playlists = [
      buildPlaylist({ id: 'pl-1', name: 'Favorites', book_ids: ['x1', 'x2'] }),
      buildPlaylist({ id: 'pl-2', name: 'To Read', book_ids: [] }),
    ];

    beforeEach(() => {
      mockListPlaylists.mockResolvedValue({
        playlists,
        count: playlists.length,
      });
    });

    it('lists existing playlists', async () => {
      renderWithProviders(<AddToPlaylistDialog open={true} onClose={onClose} bookIds={['b1']} />);
      await waitFor(() => {
        expect(screen.getByText('Favorites')).toBeInTheDocument();
        expect(screen.getByText('To Read')).toBeInTheDocument();
      });
    });

    it('shows book count for playlists', async () => {
      renderWithProviders(<AddToPlaylistDialog open={true} onClose={onClose} bookIds={['b1']} />);
      await waitFor(() => {
        expect(screen.getByText('2 books')).toBeInTheDocument();
      });
    });

    it('enables Add button after selecting a playlist', async () => {
      renderWithProviders(<AddToPlaylistDialog open={true} onClose={onClose} bookIds={['b1']} />);
      await waitFor(() => {
        expect(screen.getByText('Favorites')).toBeInTheDocument();
      });

      // Add button should be disabled initially
      expect(screen.getByRole('button', { name: 'Add' })).toBeDisabled();

      // Click a playlist to select it
      fireEvent.click(screen.getByText('Favorites'));

      // Add button should now be enabled
      expect(screen.getByRole('button', { name: 'Add' })).not.toBeDisabled();
    });

    it('calls addBooksToPlaylist when Add is clicked', async () => {
      renderWithProviders(<AddToPlaylistDialog open={true} onClose={onClose} bookIds={['b1']} />);
      await waitFor(() => {
        expect(screen.getByText('Favorites')).toBeInTheDocument();
      });

      fireEvent.click(screen.getByText('Favorites'));
      fireEvent.click(screen.getByRole('button', { name: 'Add' }));

      await waitFor(() => {
        expect(mockAddBooksToPlaylist).toHaveBeenCalledWith('pl-1', ['b1']);
        expect(onClose).toHaveBeenCalled();
      });
    });
  });

  describe('creating a new playlist', () => {
    it('enables Add button when a name is typed', async () => {
      renderWithProviders(<AddToPlaylistDialog open={true} onClose={onClose} bookIds={['b1']} />);
      await waitFor(() => {
        expect(screen.getByText('No static playlists yet.')).toBeInTheDocument();
      });

      const input = screen.getByLabelText('Or create new playlist');
      fireEvent.change(input, { target: { value: 'My New List' } });
      expect(screen.getByRole('button', { name: 'Add' })).not.toBeDisabled();
    });

    it('calls createPlaylist with the book IDs', async () => {
      renderWithProviders(
        <AddToPlaylistDialog open={true} onClose={onClose} bookIds={['b1', 'b2']} />
      );
      await waitFor(() => {
        expect(screen.getByText('No static playlists yet.')).toBeInTheDocument();
      });

      fireEvent.change(screen.getByLabelText('Or create new playlist'), {
        target: { value: 'My New List' },
      });
      fireEvent.click(screen.getByRole('button', { name: 'Add' }));

      await waitFor(() => {
        expect(mockCreatePlaylist).toHaveBeenCalledWith({
          name: 'My New List',
          type: 'static',
          book_ids: ['b1', 'b2'],
        });
        expect(onClose).toHaveBeenCalled();
      });
    });
  });

  describe('cancel', () => {
    it('calls onClose when Cancel is clicked', async () => {
      renderWithProviders(<AddToPlaylistDialog open={true} onClose={onClose} bookIds={['b1']} />);
      fireEvent.click(screen.getByRole('button', { name: 'Cancel' }));
      expect(onClose).toHaveBeenCalled();
    });
  });
});
