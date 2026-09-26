// file: web/src/components/bookdetail/CoverTextPanel.test.tsx
// version: 1.0.0
// guid: 1e7a4c29-6b3f-4d85-a0c2-9f5d3b8e1a47
// last-edited: 2026-09-26

import { act, render, screen, waitFor } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { describe, it, expect, vi, beforeEach } from 'vitest';
import { getCoverText } from '../../services/api';
import { CoverTextPanel } from './CoverTextPanel';

vi.mock('../../services/api', () => ({
  getCoverText: vi.fn(),
}));

const mocked = vi.mocked(getCoverText);

describe('CoverTextPanel', () => {
  beforeEach(() => {
    mocked.mockReset();
  });

  it('renders nothing when no cover image has been indexed', async () => {
    mocked.mockResolvedValue({ book_id: 'b1', images: [] });
    const { container } = render(<CoverTextPanel bookId="b1" />);
    await waitFor(() => expect(mocked).toHaveBeenCalledWith('b1'));
    expect(container).toBeEmptyDOMElement();
  });

  it('renders nothing when the fetch fails', async () => {
    mocked.mockImplementation(async () => {
      throw new Error('boom');
    });
    const { container } = render(<CoverTextPanel bookId="b1" />);
    await act(async () => {
      await Promise.resolve();
    });
    expect(mocked).toHaveBeenCalledTimes(1);
    expect(container).toBeEmptyDOMElement();
  });

  it('shows a collapsed "Cover text" section that expands to the stored text', async () => {
    mocked.mockResolvedValue({
      book_id: 'b1',
      images: [
        {
          hash: 'a'.repeat(64),
          source: 'folder',
          status: 'ok',
          model: 'vision-model',
          read_at: '2026-09-26T12:00:00Z',
          text: {
            title: 'The Long Road',
            subtitle: 'A Novel',
            authors: ['Jane Writer'],
            narrators: ['Sam Reader'],
            series: 'Roads',
            series_number: '2',
            other_text: ['Unabridged'],
          },
        },
        { hash: 'b'.repeat(64), source: 'embedded', status: 'error', error: 'timeout' },
      ],
    });
    render(<CoverTextPanel bookId="b1" />);

    const header = await screen.findByText('Cover text');
    expect(screen.getByText('1/2 read')).toBeInTheDocument();
    // Collapsed: the details are not visible until expanded.
    expect(screen.queryByText('The Long Road')).not.toBeVisible();

    await userEvent.click(header);
    expect(await screen.findByText('The Long Road')).toBeVisible();
    expect(screen.getByText('A Novel')).toBeInTheDocument();
    expect(screen.getByText('Jane Writer')).toBeInTheDocument();
    expect(screen.getByText('Sam Reader')).toBeInTheDocument();
    expect(screen.getByText('Roads #2')).toBeInTheDocument();
    expect(screen.getByText('Unabridged')).toBeInTheDocument();
    expect(screen.getByText('Folder image')).toBeInTheDocument();
    expect(screen.getByText(/Read failed: timeout/)).toBeInTheDocument();
  });
});
