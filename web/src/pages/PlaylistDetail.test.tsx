// file: web/src/pages/PlaylistDetail.test.tsx
// version: 1.0.0
// guid: 4b8d2e61-7a39-4c05-9e1f-c6a0d3b75f28
// last-edited: 2026-10-26

import { describe, it, expect, vi, beforeEach } from 'vitest';
import { screen, waitFor } from '@testing-library/react';
import { Route, Routes } from 'react-router-dom';
import { renderWithProviders } from '../test/renderWithProviders';
import { buildPlaylist } from '../test/factories';
import PlaylistDetail from './PlaylistDetail';

vi.mock('../services/playlistApi', () => ({
  getPlaylist: vi.fn(),
  updatePlaylist: vi.fn(),
  removeBookFromPlaylist: vi.fn(),
  materializePlaylist: vi.fn(),
  deletePlaylist: vi.fn(),
}));
vi.mock('../services/api', () => ({
  getBook: vi.fn().mockRejectedValue(new Error('not needed')),
}));

import { getPlaylist } from '../services/playlistApi';

const mockGetPlaylist = vi.mocked(getPlaylist);

function renderDetail() {
  return renderWithProviders(
    <Routes>
      <Route path="/playlists/:id" element={<PlaylistDetail />} />
      <Route path="/playlists" element={<div>the playlists list</div>} />
    </Routes>,
    { initialEntries: ['/playlists/pl-1'] }
  );
}

const smart = buildPlaylist({
  id: 'pl-1',
  name: "No Man's Land",
  type: 'smart',
  query: 'title:Hush',
});

beforeEach(() => {
  vi.clearAllMocks();
});

describe('PlaylistDetail member counts', () => {
  it('shows the plain count when the list is complete', async () => {
    mockGetPlaylist.mockResolvedValue({ playlist: smart, book_ids: ['b1', 'b2', 'b3'] });
    renderDetail();
    expect(await screen.findByText('Books (3)')).toBeInTheDocument();
  });

  it('shows "N of TOTAL" when the server cut the list to its bound', async () => {
    const listed = Array.from({ length: 10000 }, (_, i) => `s${String(i).padStart(5, '0')}`);
    mockGetPlaylist.mockResolvedValue({ playlist: smart, book_ids: listed, book_ids_total: 10300 });
    renderDetail();
    expect(await screen.findByText('Books (10,000 of 10,300)')).toBeInTheDocument();
  });

  it('does not claim a total that is not larger than the list', async () => {
    mockGetPlaylist.mockResolvedValue({ playlist: smart, book_ids: ['b1'], book_ids_total: 1 });
    renderDetail();
    expect(await screen.findByText('Books (1)')).toBeInTheDocument();
  });
});

describe('PlaylistDetail unavailable members', () => {
  it("says the members are unavailable, with the server's reason, instead of showing an empty list as the answer", async () => {
    mockGetPlaylist.mockResolvedValue({
      playlist: buildPlaylist({
        id: 'pl-1',
        name: 'Hush',
        type: 'smart',
        query: 'read_status:completed',
      }),
      book_ids: [],
      members_unavailable: true,
      members_unavailable_message:
        "This playlist's query can no longer be evaluated, so it shows no books: read_status must be one of unstarted, in_progress, finished, abandoned. Edit the query to fix it.",
    });
    renderDetail();
    await waitFor(() => {
      expect(screen.getByRole('alert')).toHaveTextContent('read_status must be one of');
    });
  });

  it('shows no alert for a playlist whose members are available', async () => {
    mockGetPlaylist.mockResolvedValue({ playlist: smart, book_ids: [] });
    renderDetail();
    await screen.findByText('Books (0)');
    expect(screen.queryByRole('alert')).not.toBeInTheDocument();
  });
});
