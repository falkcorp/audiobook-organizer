// file: web/src/pages/TrashedVersions.test.tsx
// version: 1.5.0
// guid: d74a1824-3f19-49fe-9a4f-fdbbea124f4b
// last-edited: 2026-10-29

import { afterEach, describe, expect, it, vi } from 'vitest';
import { screen } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { renderWithProviders } from '../test/renderWithProviders';
import { loginPageResponse } from '../test/loginRedirect';
import TrashedVersions from './TrashedVersions';

afterEach(() => {
  vi.unstubAllGlobals();
});

describe('TrashedVersions', () => {
  it('shows an error, not "No trashed versions.", when the session has expired', async () => {
    vi.stubGlobal(
      'fetch',
      vi.fn(async () => loginPageResponse())
    );
    renderWithProviders(<TrashedVersions />);
    expect(await screen.findByText(/session has expired/i)).toBeInTheDocument();
    expect(screen.queryByText('No trashed versions.')).not.toBeInTheDocument();
  });

  it('lists versions from the { data: { versions } } envelope', async () => {
    vi.stubGlobal(
      'fetch',
      vi.fn(
        async () =>
          new Response(
            JSON.stringify({
              data: {
                versions: [
                  {
                    id: 'v1',
                    book_id: 'book-1',
                    status: 'trash',
                    format: 'm4b',
                    source: 'test',
                    ingest_date: '2026-01-01T00:00:00Z',
                    created_at: '2026-01-01T00:00:00Z',
                  },
                ],
              },
            }),
            { status: 200, headers: { 'Content-Type': 'application/json' } }
          )
      )
    );
    renderWithProviders(<TrashedVersions />);
    expect(await screen.findByText(/m4b/i)).toBeInTheDocument();
  });

  it('dates a trash row by trashed_at, and shows a dash when a legacy row has none', async () => {
    const row = (id: string, extra: Record<string, string>) => ({
      id,
      book_id: `book-${id}`,
      status: 'trash',
      format: id === 'stamped' ? 'm4b' : 'mp3',
      source: 'test',
      ingest_date: '2026-01-01T00:00:00Z',
      created_at: '2026-01-01T00:00:00Z',
      ...extra,
    });
    vi.stubGlobal(
      'fetch',
      vi.fn(
        async () =>
          new Response(
            JSON.stringify({
              data: {
                versions: [
                  row('stamped', { trashed_at: '2026-03-15T12:00:00Z' }),
                  row('legacy', {}),
                ],
              },
            }),
            { status: 200, headers: { 'Content-Type': 'application/json' } }
          )
      )
    );
    renderWithProviders(<TrashedVersions />);
    await screen.findByText(/m4b/i);
    expect(
      screen.getByText(new Date('2026-03-15T12:00:00Z').toLocaleDateString())
    ).toBeInTheDocument();
    expect(screen.getByText('\u2014')).toBeInTheDocument();
    // The legacy row's created date must not stand in for a trash date.
    expect(
      screen.queryByText(new Date('2026-01-01T00:00:00Z').toLocaleDateString())
    ).not.toBeInTheDocument();
  });

  it('offers no Restore for a purging row, only a retry of the purge', async () => {
    const row = (id: string, status: string, format: string) => ({
      id,
      book_id: `book-${id}`,
      status,
      format,
      source: 'test',
      ingest_date: '2026-01-01T00:00:00Z',
      created_at: '2026-01-01T00:00:00Z',
    });
    vi.stubGlobal(
      'fetch',
      vi.fn(
        async () =>
          new Response(
            JSON.stringify({
              data: { versions: [row('t', 'trash', 'm4b'), row('p', 'purging', 'mp3')] },
            }),
            { status: 200, headers: { 'Content-Type': 'application/json' } }
          )
      )
    );
    renderWithProviders(<TrashedVersions />);
    await screen.findByText(/m4b/i);
    // The trash row keeps Restore (the control: the button is not simply gone).
    expect(screen.getAllByRole('button', { name: 'Restore' })).toHaveLength(1);
    expect(screen.getByRole('button', { name: 'Purge Now' })).toBeInTheDocument();
    // The purging row can be retried but never restored.
    expect(screen.getByRole('button', { name: 'Retry Purge' })).toBeInTheDocument();
    expect(screen.getByText('purging')).toBeInTheDocument();
    // Its created date must not stand in for a trash date.
    expect(
      screen.queryByText(new Date('2026-01-01T00:00:00Z').toLocaleDateString())
    ).not.toBeInTheDocument();
  });

  const versionRow = (
    id: string,
    status: string,
    format: string,
    extra: Record<string, unknown> = {}
  ) => ({
    id,
    book_id: `book-${id}`,
    status,
    format,
    source: 'test',
    ingest_date: '2026-01-01T00:00:00Z',
    created_at: '2026-01-01T00:00:00Z',
    ...extra,
  });
  const listResponse = (versions: unknown[]) =>
    new Response(JSON.stringify({ data: { versions, count: versions.length } }), {
      status: 200,
      headers: { 'Content-Type': 'application/json' },
    });

  it('shows a purge_failed row with what failed, and offers a retry and a hard delete but no restore', async () => {
    vi.stubGlobal(
      'fetch',
      vi.fn(async () =>
        listResponse([
          versionRow('f', 'purge_failed', 'm4b', {
            purge_failed: [
              { path: '/lib/Gotham/Hush/hush.m4b', reason: 'operation not permitted' },
            ],
            purge_failed_at: '2026-10-20T00:00:00Z',
          }),
        ])
      )
    );
    renderWithProviders(<TrashedVersions />);
    await screen.findByText(/m4b/i);
    expect(screen.getByText('purge_failed')).toBeInTheDocument();
    const note = screen.getByText('1 file could not be removed');
    expect(note).toHaveAttribute('title', '/lib/Gotham/Hush/hush.m4b: operation not permitted');
    expect(screen.getByRole('button', { name: 'Retry Purge' })).toBeInTheDocument();
    expect(screen.getByRole('button', { name: 'Hard Delete' })).toBeInTheDocument();
    expect(screen.queryByRole('button', { name: 'Restore' })).not.toBeInTheDocument();
    expect(screen.queryByRole('button', { name: 'Purge Now' })).not.toBeInTheDocument();
  });

  it('a purging row keeps Retry Purge only: the nightly cleanup retries it, there is nothing to give up on', async () => {
    vi.stubGlobal(
      'fetch',
      vi.fn(async () => listResponse([versionRow('p', 'purging', 'mp3')]))
    );
    renderWithProviders(<TrashedVersions />);
    await screen.findByText(/mp3/i);
    expect(screen.getByRole('button', { name: 'Retry Purge' })).toBeInTheDocument();
    expect(screen.queryByRole('button', { name: 'Hard Delete' })).not.toBeInTheDocument();
  });

  it('says how many files a finished purge kept, with the paths and reasons on hover', async () => {
    vi.stubGlobal(
      'fetch',
      vi.fn(async () =>
        listResponse([
          versionRow('k', 'inactive_purged', 'm4b', {
            purge_kept: [
              { path: '/lib/Gotham/Knightfall/part1.m4b', reason: 'another book names it' },
              { path: '/lib/Gotham/Knightfall/part2.m4b', reason: 'another book names it' },
            ],
          }),
        ])
      )
    );
    renderWithProviders(<TrashedVersions />);
    await screen.findByText(/m4b/i);
    const note = screen.getByText('2 files kept');
    expect(note.getAttribute('title')).toContain(
      '/lib/Gotham/Knightfall/part2.m4b: another book names it'
    );
  });

  it('shows why a purge-now was refused instead of dropping the rejection', async () => {
    const fetchMock = vi.fn(async (_url: string, init?: RequestInit) => {
      if (init?.method === 'POST') {
        return new Response(
          JSON.stringify({ error: 'a merge or combine is running; nothing was deleted' }),
          {
            status: 409,
            headers: { 'Content-Type': 'application/json' },
          }
        );
      }
      return listResponse([versionRow('t', 'trash', 'm4b')]);
    });
    vi.stubGlobal('fetch', fetchMock);
    vi.stubGlobal('confirm', () => true);
    renderWithProviders(<TrashedVersions />);
    await screen.findByText(/m4b/i);
    await userEvent.click(screen.getByRole('button', { name: 'Purge Now' }));
    expect(
      await screen.findByText(/Purge failed: a merge or combine is running/)
    ).toBeInTheDocument();
  });

  // A trash cleanup run's counts link here as ?op=<id>&set=<group>: the page
  // lists only that group's versions, read from the run's stored result.
  describe('narrowed to a trash cleanup run', () => {
    const resultResponse = (result: unknown) =>
      new Response(JSON.stringify({ data: { result_data: result } }), {
        status: 200,
        headers: { 'Content-Type': 'application/json' },
      });
    const routed = (byUrl: Record<string, () => Response>) =>
      vi.fn(async (url: string) => {
        const hit = Object.keys(byUrl).find((k) => String(url).includes(k));
        if (!hit) throw new Error(`unrouted request ${String(url)}`);
        return byUrl[hit]();
      });

    it('lists only the kept versions of the run, on the Purged tab, with the book linked', async () => {
      const fetchMock = routed({
        '/operations/op-night/result': () =>
          resultResponse({
            kept_files: 1,
            kept_version_ids: ['v-kept'],
            kept_book_ids: ['book-kept'],
          }),
        '/audiobooks/purged-versions': () =>
          listResponse([
            versionRow('v-kept', 'inactive_purged', 'm4b'),
            versionRow('v-other', 'inactive_purged', 'mp3'),
          ]),
      });
      vi.stubGlobal('fetch', fetchMock);
      renderWithProviders(<TrashedVersions />, {
        initialEntries: ['/versions?op=op-night&set=kept'],
      });
      expect(await screen.findByText(/m4b/i)).toBeInTheDocument();
      // The control: the other purged version is on the list but not in the run.
      expect(screen.queryByText(/mp3/i)).not.toBeInTheDocument();
      expect(
        screen.getByText(/Showing the 1 version that cleanup run purged with files kept/)
      ).toBeInTheDocument();
      expect(screen.getByRole('link', { name: 'book-v-kept…' })).toHaveAttribute(
        'href',
        '/library/book-v-kept'
      );
    });

    it('opens the Trash tab for the versions that need attention and says what is not on the tab', async () => {
      const fetchMock = routed({
        '/operations/op-night/result': () =>
          resultResponse({
            needs_attention: 2,
            needs_attention_version_ids: ['v-stuck', 'v-hard-deleted'],
          }),
        '/audiobooks/trashed-versions': () =>
          listResponse([
            versionRow('v-stuck', 'purge_failed', 'm4b'),
            versionRow('v-fine', 'trash', 'mp3'),
          ]),
      });
      vi.stubGlobal('fetch', fetchMock);
      renderWithProviders(<TrashedVersions />, {
        initialEntries: ['/versions?op=op-night&set=needs_attention'],
      });
      expect(await screen.findByText(/m4b/i)).toBeInTheDocument();
      expect(screen.queryByText(/mp3/i)).not.toBeInTheDocument();
      expect(
        screen.getByText(
          /Showing the 2 versions that cleanup run could not purge because a file cannot be removed\. It does not retry them/
        )
      ).toBeInTheDocument();
      expect(
        screen.getByText(/1 is not on this tab: hard deleted since, or on the other tab/)
      ).toBeInTheDocument();
    });

    // Only needs_attention is left alone; the other trash groups are tried
    // again by every run, and the banner must not say otherwise.
    it.each([
      [
        'cannot_tell',
        /left alone because it could not tell whether their files are gone.*next run tries them again/,
      ],
      [
        'nothing_served',
        /left in the trash because their book serves nothing that exists on disk.*every run lists them again until the book's path is fixed/,
      ],
      [
        'served_kept',
        /kept in the trash because their book still plays one of their files.*every run tries them again/,
      ],
    ])('says what the %s group means and that the run retries it', async (set, banner) => {
      const fetchMock = routed({
        '/operations/op-night/result': () => resultResponse({ [`${set}_version_ids`]: ['v-held'] }),
        '/audiobooks/trashed-versions': () =>
          listResponse([
            versionRow('v-held', 'trash', 'm4b'),
            versionRow('v-fine', 'trash', 'mp3'),
          ]),
      });
      vi.stubGlobal('fetch', fetchMock);
      renderWithProviders(<TrashedVersions />, {
        initialEntries: [`/versions?op=op-night&set=${set}`],
      });
      expect(await screen.findByText(/m4b/i)).toBeInTheDocument();
      const text = screen.getByText(/Showing the 1 version/).textContent ?? '';
      expect(text).toMatch(banner);
      expect(text).not.toMatch(/does not retry/);
    });

    it('Show all drops the narrowing and lists every version again', async () => {
      const fetchMock = routed({
        '/operations/op-night/result': () =>
          resultResponse({ kept_files: 1, kept_version_ids: ['v-kept'] }),
        '/audiobooks/purged-versions': () =>
          listResponse([
            versionRow('v-kept', 'inactive_purged', 'm4b'),
            versionRow('v-other', 'inactive_purged', 'mp3'),
          ]),
      });
      vi.stubGlobal('fetch', fetchMock);
      renderWithProviders(<TrashedVersions />, {
        initialEntries: ['/versions?op=op-night&set=kept'],
      });
      await screen.findByText(/m4b/i);
      await userEvent.click(screen.getByRole('button', { name: 'Show all' }));
      expect(await screen.findByText(/mp3/i)).toBeInTheDocument();
      expect(screen.queryByText(/Showing the/)).not.toBeInTheDocument();
    });

    it('shows every version and says why when the run result cannot be read', async () => {
      const fetchMock = routed({
        '/operations/op-gone/result': () =>
          new Response(JSON.stringify({ error: 'operation not found' }), {
            status: 404,
            headers: { 'Content-Type': 'application/json' },
          }),
        '/audiobooks/purged-versions': () =>
          listResponse([versionRow('v-other', 'inactive_purged', 'mp3')]),
      });
      vi.stubGlobal('fetch', fetchMock);
      renderWithProviders(<TrashedVersions />, {
        initialEntries: ['/versions?op=op-gone&set=kept'],
      });
      expect(await screen.findByText(/mp3/i)).toBeInTheDocument();
      expect(screen.getByText(/Showing every version instead/)).toBeInTheDocument();
    });
  });
  // The third tab: books whose version swap did not finish, with a Resume button.
  describe('Unfinished swaps tab', () => {
    const stuckBook = (id: string, extra: Record<string, unknown> = {}) => ({
      book_id: id,
      title: 'The Long Halloween',
      book_path: '/lib/Gotham/The Long Halloween',
      versions: [
        {
          id: `${id}-out`,
          status: 'swapping_out',
          files: [{ id: 'f1', path: '/lib/Gotham/The Long Halloween/part1.m4b' }],
        },
        {
          id: `${id}-in`,
          status: 'swapping_in',
          files: [{ id: 'f2', path: '/lib/Gotham/The Long Halloween/.versions/x/part1.mp3' }],
        },
      ],
      resume: 'finish',
      ...extra,
    });
    const swapsResponse = (books: unknown[]) =>
      new Response(JSON.stringify({ data: { books, count: books.length } }), {
        status: 200,
        headers: { 'Content-Type': 'application/json' },
      });
    const json = (status: number, body: unknown) =>
      new Response(JSON.stringify(body), {
        status,
        headers: { 'Content-Type': 'application/json' },
      });

    it('lists each book with its path, versions and files, and a Resume button', async () => {
      vi.stubGlobal(
        'fetch',
        vi.fn(async (url: string) =>
          String(url).includes('/audiobooks/swap-incomplete')
            ? swapsResponse([stuckBook('book-lh')])
            : listResponse([])
        )
      );
      renderWithProviders(<TrashedVersions />);
      await userEvent.click(await screen.findByRole('tab', { name: 'Unfinished swaps' }));

      const row = await screen.findByTestId('unfinished-swap-book-lh');
      expect(row).toHaveTextContent('The Long Halloween');
      expect(row).toHaveTextContent('/lib/Gotham/The Long Halloween');
      expect(row).toHaveTextContent('swapping_out');
      expect(row).toHaveTextContent('swapping_in');
      expect(row).toHaveTextContent('part1.m4b');
      expect(screen.getByRole('button', { name: 'Resume (finish swap)' })).toBeInTheDocument();
    });

    it('resumes a book after a confirm, shows what happened and reloads the list', async () => {
      let resumed = false;
      const fetchMock = vi.fn(async (url: string, init?: RequestInit) => {
        if (String(url).includes('/audiobooks/book-lh/versions/resume-swap')) {
          expect(init?.method).toBe('POST');
          resumed = true;
          return json(200, {
            data: { action: 'finish', book_id: 'book-lh', book_path: '/lib', files_moved: 0 },
          });
        }
        if (String(url).includes('/audiobooks/swap-incomplete')) {
          return swapsResponse(resumed ? [] : [stuckBook('book-lh')]);
        }
        return listResponse([]);
      });
      vi.stubGlobal('fetch', fetchMock);
      const confirmMock = vi.fn(() => true);
      vi.stubGlobal('confirm', confirmMock);
      renderWithProviders(<TrashedVersions />);
      await userEvent.click(await screen.findByRole('tab', { name: 'Unfinished swaps' }));

      await userEvent.click(await screen.findByRole('button', { name: 'Resume (finish swap)' }));

      expect(confirmMock).toHaveBeenCalledTimes(1);
      expect(String(confirmMock.mock.calls[0])).toContain('No file is moved');
      expect(
        await screen.findByText(/The Long Halloween: The swap was finished/)
      ).toBeInTheDocument();
      expect(await screen.findByText('No unfinished swaps.')).toBeInTheDocument();
    });

    it('sends nothing when the confirm is declined', async () => {
      const fetchMock = vi.fn(async (url: string) =>
        String(url).includes('/audiobooks/swap-incomplete')
          ? swapsResponse([stuckBook('book-lh')])
          : listResponse([])
      );
      vi.stubGlobal('fetch', fetchMock);
      vi.stubGlobal('confirm', () => false);
      renderWithProviders(<TrashedVersions />);
      await userEvent.click(await screen.findByRole('tab', { name: 'Unfinished swaps' }));
      await userEvent.click(await screen.findByRole('button', { name: 'Resume (finish swap)' }));
      expect(fetchMock.mock.calls.some(([u]) => String(u).includes('resume-swap'))).toBe(false);
    });

    it('says what an undo does and shows why a refused resume failed', async () => {
      const fetchMock = vi.fn(async (url: string) => {
        if (String(url).includes('resume-swap')) {
          return json(409, {
            error: 'version ver-other is active as well',
            code: 'SWAP_RESUME_REFUSED',
            status: 409,
          });
        }
        if (String(url).includes('/audiobooks/swap-incomplete')) {
          return swapsResponse([stuckBook('book-lh', { resume: 'undo' })]);
        }
        return listResponse([]);
      });
      vi.stubGlobal('fetch', fetchMock);
      const confirmMock = vi.fn(() => true);
      vi.stubGlobal('confirm', confirmMock);
      renderWithProviders(<TrashedVersions />);
      await userEvent.click(await screen.findByRole('tab', { name: 'Unfinished swaps' }));

      await userEvent.click(
        await screen.findByRole('button', { name: 'Resume (move files back)' })
      );

      expect(String(confirmMock.mock.calls[0])).toContain('puts the files back');
      expect(
        await screen.findByText(/Resume failed: version ver-other is active as well/)
      ).toBeInTheDocument();
      // The book is still listed: nothing was resumed.
      expect(screen.getByTestId('unfinished-swap-book-lh')).toBeInTheDocument();
    });

    it('offers no Resume for a book the server would refuse, and says why', async () => {
      vi.stubGlobal(
        'fetch',
        vi.fn(async (url: string) =>
          String(url).includes('/audiobooks/swap-incomplete')
            ? swapsResponse([
                stuckBook('book-lh', {
                  resume: undefined,
                  resume_refusal: 'a file is at the book folder and in its slot',
                }),
              ])
            : listResponse([])
        )
      );
      renderWithProviders(<TrashedVersions />);
      await userEvent.click(await screen.findByRole('tab', { name: 'Unfinished swaps' }));

      expect(await screen.findByTestId('resume-refusal')).toHaveTextContent(
        'a file is at the book folder and in its slot'
      );
      expect(screen.queryByRole('button', { name: /Resume/ })).not.toBeInTheDocument();
    });

    it('opens from a cleanup run, lists only its books and says the run never resumes', async () => {
      vi.stubGlobal(
        'fetch',
        vi.fn(async (url: string) => {
          if (String(url).includes('/operations/op-night/result')) {
            return json(200, {
              data: {
                result_data: {
                  swap_incomplete: 2,
                  swap_incomplete_version_ids: ['book-lh-out', 'book-lh-in', 'gone-ver'],
                },
              },
            });
          }
          if (String(url).includes('/audiobooks/swap-incomplete')) {
            return swapsResponse([stuckBook('book-lh'), stuckBook('book-other')]);
          }
          return listResponse([]);
        })
      );
      renderWithProviders(<TrashedVersions />, {
        initialEntries: ['/versions?op=op-night&set=swap_incomplete'],
      });

      expect(await screen.findByTestId('unfinished-swap-book-lh')).toBeInTheDocument();
      expect(screen.queryByTestId('unfinished-swap-book-other')).not.toBeInTheDocument();
      expect(screen.getByText(/does not resume a swap/)).toBeInTheDocument();
      expect(
        screen.getByText(/1 version of that cleanup run is not in an unfinished swap any more/)
      ).toBeInTheDocument();
    });

    it('shows the load error instead of an empty list', async () => {
      vi.stubGlobal(
        'fetch',
        vi.fn(async (url: string) =>
          String(url).includes('/audiobooks/swap-incomplete')
            ? json(500, { error: 'boom' })
            : listResponse([])
        )
      );
      renderWithProviders(<TrashedVersions />);
      await userEvent.click(await screen.findByRole('tab', { name: 'Unfinished swaps' }));
      expect(await screen.findByText('Failed to load the unfinished swaps')).toBeInTheDocument();
      expect(screen.queryByText('No unfinished swaps.')).not.toBeInTheDocument();
    });
  });
});
