// file: web/src/components/operations/TrashCleanupResultPanel.test.tsx
// version: 1.5.0
// guid: 6255d3b3-6c19-482d-a750-5e14a4ad74e5
// last-edited: 2026-10-29

import { beforeEach, describe, expect, it, vi } from 'vitest';
import { screen } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { renderWithProviders } from '../../test/renderWithProviders';
import TrashCleanupResultPanel from './TrashCleanupResultPanel';
import { getOperationResult } from '../../services/api';

vi.mock('../../services/api', () => ({ getOperationResult: vi.fn() }));
const mockedResult = vi.mocked(getOperationResult);

beforeEach(() => {
  vi.clearAllMocks();
});

describe('TrashCleanupResultPanel', () => {
  it('links the kept count to the kept versions and each book to its page', async () => {
    mockedResult.mockResolvedValue({
      result_data: {
        purged: 5,
        failed: 0,
        kept_files: 2,
        kept_version_ids: ['v-kept-1', 'v-kept-2'],
        kept_book_ids: ['book-year-one', 'book-year-one'],
        kept_file_paths: ['/lib/Gotham/Year One/part1.m4b', '/lib/Gotham/Year One/part2.m4b'],
      },
    });
    renderWithProviders(<TrashCleanupResultPanel opId="op-night-1" />);

    const line = await screen.findByTestId('trash-cleanup-kept');
    const count = screen.getByRole('link', { name: '2 versions' });
    expect(count).toHaveAttribute('href', '/versions?op=op-night-1&set=kept');
    expect(line).toHaveTextContent('purged with files left on disk');
    // Two versions of one book list the book once.
    const bookLinks = screen.getAllByRole('link', { name: 'book-year-on' });
    expect(bookLinks).toHaveLength(1);
    expect(bookLinks[0]).toHaveAttribute('href', '/library/book-year-one');
    expect(screen.queryByTestId('trash-cleanup-needs_attention')).not.toBeInTheDocument();
  });

  it('shows the kept file paths behind a button that carries their count', async () => {
    mockedResult.mockResolvedValue({
      result_data: {
        purged: 1,
        kept_files: 1,
        kept_version_ids: ['v1'],
        kept_book_ids: ['b1'],
        kept_file_paths: ['/lib/Gotham/Year One/part1.m4b', '/lib/Gotham/Year One/part2.m4b'],
      },
    });
    renderWithProviders(<TrashCleanupResultPanel opId="op-1" />);
    const button = await screen.findByRole('button', { name: '2 files kept on disk' });
    expect(screen.queryByText('/lib/Gotham/Year One/part1.m4b')).not.toBeInTheDocument();
    await userEvent.setup().click(button);
    expect(await screen.findByText('/lib/Gotham/Year One/part1.m4b')).toBeInTheDocument();
    expect(screen.getByText('/lib/Gotham/Year One/part2.m4b')).toBeInTheDocument();
  });

  it('links the cannot-tell count to its versions and says nothing was deleted', async () => {
    mockedResult.mockResolvedValue({
      result_data: {
        purged: 0,
        cannot_tell: 1,
        cannot_tell_version_ids: ['v-down'],
        cannot_tell_book_ids: ['book-long-halloween'],
      },
    });
    renderWithProviders(<TrashCleanupResultPanel opId="op-night-3" />);

    const line = await screen.findByTestId('trash-cleanup-cannot_tell');
    expect(screen.getByRole('link', { name: '1 version' })).toHaveAttribute(
      'href',
      '/versions?op=op-night-3&set=cannot_tell'
    );
    expect(line).toHaveTextContent('left alone');
    expect(line).toHaveTextContent('Nothing was deleted');
    // It is the drive-down case: the book-path wording belongs to nothing_served.
    expect(line).not.toHaveTextContent('serves nothing');
  });

  it('gives a book that serves nothing on disk its own line, with the book path to fix', async () => {
    mockedResult.mockResolvedValue({
      result_data: {
        purged: 0,
        nothing_served: 1,
        nothing_served_version_ids: ['v-orphan'],
        nothing_served_book_ids: ['book-zero-year'],
        nothing_served_file_paths: ['/lib/Gotham/Zero Year/zero-year.m4b'],
      },
    });
    renderWithProviders(<TrashCleanupResultPanel opId="op-night-5" />);

    const line = await screen.findByTestId('trash-cleanup-nothing_served');
    expect(screen.getByRole('link', { name: '1 version' })).toHaveAttribute(
      'href',
      '/versions?op=op-night-5&set=nothing_served'
    );
    expect(line).toHaveTextContent('left in the trash');
    expect(line).toHaveTextContent('the book serves nothing that exists on disk');
    expect(line).toHaveTextContent("Fix the book's path");
    expect(line).toHaveTextContent('Nothing was deleted');
    // Not the drive-down wording, and not filed under cannot-tell.
    expect(line).not.toHaveTextContent('drive that is down');
    expect(screen.queryByTestId('trash-cleanup-cannot_tell')).not.toBeInTheDocument();

    const button = screen.getByRole('button', { name: '1 book path to fix' });
    expect(screen.queryByText('/lib/Gotham/Zero Year/zero-year.m4b')).not.toBeInTheDocument();
    await userEvent.setup().click(button);
    expect(await screen.findByText('/lib/Gotham/Zero Year/zero-year.m4b')).toBeInTheDocument();
  });

  it('links the served-file count to its versions and says nothing was deleted', async () => {
    mockedResult.mockResolvedValue({
      result_data: {
        purged: 0,
        served_kept: 1,
        served_kept_version_ids: ['v-played'],
        served_kept_book_ids: ['book-year-one'],
        served_kept_file_paths: ['/lib/Gotham/Year One/part1.m4b'],
      },
    });
    renderWithProviders(<TrashCleanupResultPanel opId="op-night-4" />);

    const line = await screen.findByTestId('trash-cleanup-served_kept');
    expect(screen.getByRole('link', { name: '1 version' })).toHaveAttribute(
      'href',
      '/versions?op=op-night-4&set=served_kept'
    );
    expect(line).toHaveTextContent('kept in the trash');
    expect(line).toHaveTextContent('the book still plays its file');
    expect(line).toHaveTextContent('Nothing was deleted');
  });

  it('lists the books of an unfinished swap with their paths and says nothing was moved', async () => {
    mockedResult.mockResolvedValue({
      result_data: {
        purged: 0,
        swap_incomplete: 2,
        swap_incomplete_version_ids: ['v-out', 'v-in'],
        swap_incomplete_book_ids: ['book-long-halloween'],
        swap_incomplete_file_paths: ['/lib/Gotham/The Long Halloween'],
      },
    });
    renderWithProviders(<TrashCleanupResultPanel opId="op-night-6" />);

    const line = await screen.findByTestId('trash-cleanup-swap_incomplete');
    expect(screen.getByRole('link', { name: '2 versions' })).toHaveAttribute(
      'href',
      '/versions?op=op-night-6&set=swap_incomplete'
    );
    expect(line).toHaveTextContent('swap that did not finish');
    expect(line).toHaveTextContent('no active version');
    expect(line).toHaveTextContent('moved nothing');
    expect(line).toHaveTextContent('Unfinished swaps');
    // It is no trash group.
    expect(line).not.toHaveTextContent('left in the trash');

    const button = screen.getByRole('button', { name: '1 book path to repair' });
    expect(screen.queryByText('/lib/Gotham/The Long Halloween')).not.toBeInTheDocument();
    await userEvent.setup().click(button);
    expect(await screen.findByText('/lib/Gotham/The Long Halloween')).toBeInTheDocument();
  });

  it('shows no unfinished-swap line when none is stuck', async () => {
    mockedResult.mockResolvedValue({ result_data: { purged: 1 } });
    renderWithProviders(<TrashCleanupResultPanel opId="op-night-7" />);

    await screen.findByTestId('trash-cleanup-result');
    expect(screen.queryByTestId('trash-cleanup-swap_incomplete')).not.toBeInTheDocument();
  });

  it('links the needs-attention count to its versions and explains it', async () => {
    mockedResult.mockResolvedValue({
      result_data: {
        purged: 0,
        needs_attention: 1,
        needs_attention_version_ids: ['v-stuck'],
        needs_attention_book_ids: ['book-long-halloween'],
      },
    });
    renderWithProviders(<TrashCleanupResultPanel opId="op-night-2" />);

    const line = await screen.findByTestId('trash-cleanup-needs_attention');
    expect(screen.getByRole('link', { name: '1 version' })).toHaveAttribute(
      'href',
      '/versions?op=op-night-2&set=needs_attention'
    );
    expect(line).toHaveTextContent('needs attention');
    expect(line).toHaveTextContent('does not retry it');
    expect(screen.getByRole('link', { name: 'book-long-ha' })).toHaveAttribute(
      'href',
      '/library/book-long-halloween'
    );
    expect(screen.queryByTestId('trash-cleanup-kept')).not.toBeInTheDocument();
  });

  it('sends the books past the cap to the filtered versions page', async () => {
    const ids = Array.from({ length: 13 }, (_, i) => `book-${String(i).padStart(2, '0')}`);
    mockedResult.mockResolvedValue({
      result_data: {
        purged: 13,
        kept_files: 13,
        kept_version_ids: ids.map((id) => `v-${id}`),
        kept_book_ids: ids,
      },
    });
    renderWithProviders(<TrashCleanupResultPanel opId="op-big" />);
    await screen.findByTestId('trash-cleanup-kept');
    const more = screen.getByRole('link', { name: '3 more' });
    expect(more).toHaveAttribute('href', '/versions?op=op-big&set=kept');
  });

  it('says a merge stopped the run, in plain words, with no link', async () => {
    mockedResult.mockResolvedValue({ result_data: { purged: 2, stopped_for_merge: true } });
    renderWithProviders(<TrashCleanupResultPanel opId="op-3" />);
    const text = await screen.findByText(/Stopped early: a merge or combine was running/);
    expect(text).toHaveTextContent('left for the next run');
    expect(text).toHaveTextContent('Nothing failed');
    expect(text.closest('a')).toBeNull();
  });

  it('warns, without a link or a failure, when the run has stopped for the merge lock night after night', async () => {
    mockedResult.mockResolvedValue({
      result_data: {
        purged: 0,
        stopped_for_merge: true,
        stopped_for_merge_nights: 4,
        stopped_for_merge_repeatedly: true,
      },
    });
    renderWithProviders(<TrashCleanupResultPanel opId="op-stuck" />);
    const warn = await screen.findByTestId('trash-cleanup-merge-stuck');
    expect(warn).toHaveTextContent('4 nights in a row');
    expect(warn).toHaveTextContent('the trash is not being purged');
    expect(warn.closest('a')).toBeNull();
  });

  it('says nothing about a streak on a single stopped night', async () => {
    mockedResult.mockResolvedValue({
      result_data: { purged: 2, stopped_for_merge: true, stopped_for_merge_nights: 1 },
    });
    renderWithProviders(<TrashCleanupResultPanel opId="op-once" />);
    await screen.findByText(/Stopped early: a merge or combine was running/);
    expect(screen.queryByTestId('trash-cleanup-merge-stuck')).not.toBeInTheDocument();
  });

  it('says how many failed and points at the log, not at a link it cannot back', async () => {
    mockedResult.mockResolvedValue({ result_data: { purged: 1, failed: 2 } });
    renderWithProviders(<TrashCleanupResultPanel opId="op-4" />);
    expect(await screen.findByText(/2 could not be purged/)).toBeInTheDocument();
    expect(screen.queryByRole('link')).not.toBeInTheDocument();
  });

  it('renders nothing when the run stored no result', async () => {
    mockedResult.mockRejectedValue(new Error('no result'));
    const { container } = renderWithProviders(<TrashCleanupResultPanel opId="op-5" />);
    await vi.waitFor(() => expect(mockedResult).toHaveBeenCalledWith('op-5'));
    expect(container).toBeEmptyDOMElement();
  });
  it('lists what a report-only kept-file sweep would remove, with version, book and reason', async () => {
    mockedResult.mockResolvedValue({
      result_data: {
        purged: 0,
        kept_sweep_report_only: true,
        kept_sweep_would_remove: 1,
        kept_sweep_would_drop: 1,
        kept_sweep_still_kept: 4,
        kept_sweep_would_remove_files: [
          {
            version_id: 'v-year-one-0001',
            book_id: 'book-year-one',
            path: '/lib/Gotham/Year One/.versions/v1/part1.m4b',
            reason: 'book_file x of book y still names it',
          },
        ],
        kept_sweep_would_drop_files: [
          {
            version_id: 'v-hush-0002',
            book_id: 'book-hush',
            path: '/lib/Gotham/Hush/gone.m4b',
            reason: 'kept once',
          },
        ],
      },
    });
    renderWithProviders(<TrashCleanupResultPanel opId="op-night-9" />);

    const report = await screen.findByTestId('trash-cleanup-kept-sweep-report');
    expect(report).toHaveTextContent('report only (nothing was changed)');
    expect(report).toHaveTextContent('it would remove 1 file');
    expect(report).toHaveTextContent('4 still owned');
    expect(screen.queryByTestId('trash-cleanup-kept-sweep-applied')).not.toBeInTheDocument();

    const button = screen.getByRole('button', { name: '1 file it would remove' });
    expect(
      screen.queryByText('/lib/Gotham/Year One/.versions/v1/part1.m4b')
    ).not.toBeInTheDocument();
    await userEvent.setup().click(button);
    expect(
      await screen.findByText('/lib/Gotham/Year One/.versions/v1/part1.m4b')
    ).toBeInTheDocument();
    const entry = screen.getByTestId('trash-cleanup-kept-sweep-would-remove');
    expect(entry).toHaveTextContent('version v-year-one-');
    expect(entry).toHaveTextContent('kept because: book_file x of book y still names it');
    expect(screen.getByRole('link', { name: 'book-year-on' })).toHaveAttribute(
      'href',
      '/library/book-year-one'
    );
    expect(
      screen.getByRole('button', { name: '1 note of already-gone files it would drop' })
    ).toBeInTheDocument();
  });

  it('says what an applying sweep removed, and flags a version it escalated', async () => {
    mockedResult.mockResolvedValue({
      result_data: {
        purged: 0,
        kept_sweep_removed: 2,
        kept_sweep_gone: 1,
        kept_sweep_escalated: 1,
        kept_sweep_escalated_version_ids: ['v-stuck'],
        kept_sweep_escalated_files: [
          {
            version_id: 'v-stuck',
            book_id: 'book-hush',
            path: '/lib/Gotham/Hush/stuck.m4b',
            reason: 'owner lookup down',
          },
        ],
      },
    });
    renderWithProviders(<TrashCleanupResultPanel opId="op-night-10" />);

    const applied = await screen.findByTestId('trash-cleanup-kept-sweep-applied');
    expect(applied).toHaveTextContent('removed 2 files');
    expect(applied).toHaveTextContent('dropped 1 note');
    expect(screen.queryByTestId('trash-cleanup-kept-sweep-report')).not.toBeInTheDocument();
    expect(screen.getByTestId('trash-cleanup-kept-sweep-escalated')).toHaveTextContent(
      '1 version became purge failed'
    );
  });

  it('shows no kept-file sweep section for a run without one', async () => {
    mockedResult.mockResolvedValue({ result_data: { purged: 2 } });
    renderWithProviders(<TrashCleanupResultPanel opId="op-night-11" />);
    await screen.findByTestId('trash-cleanup-result');
    expect(screen.queryByTestId('trash-cleanup-kept-sweep')).not.toBeInTheDocument();
  });
});
