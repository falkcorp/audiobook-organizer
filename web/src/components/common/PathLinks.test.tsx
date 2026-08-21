// file: web/src/components/common/PathLinks.test.tsx
// version: 1.1.0
// guid: 19ec3b3a-a184-4122-953f-32ebd321116c
// last-edited: 2026-08-21

import { describe, it, expect, vi, beforeEach } from 'vitest';
import { render, screen, waitFor } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { PathLinks, usePathAliases } from './PathLinks';
import { getConfig, type Config, type PathAlias } from '../../services/api';

// A real root_dir so usePathVars (via the shared config fetch) abbreviates
// the posix display to $(books)/... -- without this, display and copyText
// are identical strings and the "copies the literal path, not the
// abbreviated display" test below cannot fail even if the component copies
// the wrong field.
vi.mock('../../services/api', () => ({
  getConfig: vi.fn().mockResolvedValue({ root_dir: '/library/books/audiobooks' }),
}));

// useToast() returns a real no-op { toast: () => {} } when no ToastProvider
// is mounted (ToastProvider.tsx's default context), which would make a copy-
// failure test pass whether or not the component actually calls toast(). Mock
// it so the failure test can assert what was surfaced, not just that nothing
// threw.
const { toastSpy } = vi.hoisted(() => ({ toastSpy: vi.fn() }));
vi.mock('../toast/ToastProvider', () => ({
  useToast: () => ({ toast: toastSpy }),
}));

const ALIASES: PathAlias[] = [
  { root: '/library/books', windows: 'W:', unc: '\\\\host\\books', smb_url: 'smb://host/books' },
];
const P = '/library/books/Author/Title/x.m4b';

beforeEach(() => {
  toastSpy.mockClear();
  Object.assign(navigator, { clipboard: { writeText: vi.fn().mockResolvedValue(undefined) } });
});

describe('PathLinks', () => {
  it('renders an anchor for the posix line on a handler platform', () => {
    render(<PathLinks path={P} aliases={ALIASES} platform="macOS" />);
    expect(screen.getByRole('link')).toHaveAttribute(
      'href',
      'smb://host/books/Author/Title/x.m4b',
    );
  });

  it('renders no anchor at all on Windows', () => {
    render(<PathLinks path={P} aliases={ALIASES} platform="Win32" />);
    expect(screen.queryByRole('link')).toBeNull();
    expect(screen.getByText(/W:\\Author\\Title\\x\.m4b/)).toBeInTheDocument();
  });

  it('copies the literal path, not the abbreviated display', async () => {
    render(<PathLinks path={P} aliases={ALIASES} platform="macOS" />);
    // Wait for the mocked config fetch to resolve so the posix display has
    // actually abbreviated to $(books)/... -- otherwise display === copyText
    // before hydration and a component that copies `display` would pass too.
    await screen.findByText(/\$\(books\)/);
    await userEvent.click(screen.getByRole('button', { name: /copy linux path/i }));
    expect(navigator.clipboard.writeText).toHaveBeenCalledWith(P);
  });

  it('gives every rendering its own copy button', () => {
    render(<PathLinks path={P} aliases={ALIASES} platform="macOS" />);
    expect(screen.getAllByRole('button', { name: /copy/i })).toHaveLength(3);
  });

  it('renders a single line when no alias matches', () => {
    render(<PathLinks path="/elsewhere/x.m4b" aliases={ALIASES} platform="macOS" />);
    expect(screen.getAllByRole('button', { name: /copy/i })).toHaveLength(1);
    expect(screen.queryByRole('link')).toBeNull();
  });

  it('surfaces a failure toast when the clipboard write rejects, not a silent no-op', async () => {
    Object.assign(navigator, {
      clipboard: { writeText: vi.fn().mockRejectedValue(new Error('denied')) },
    });
    render(<PathLinks path={P} aliases={ALIASES} platform="macOS" />);
    await userEvent.click(screen.getByRole('button', { name: /copy linux path/i }));
    // onClick is fire-and-forget (`() => void handleCopy(r)`), so the
    // rejection settles after the click resolves -- wait rather than assert
    // synchronously.
    await waitFor(() => {
      expect(toastSpy).toHaveBeenCalledWith(expect.stringContaining('Copy failed'), 'error');
    });
  });
});

function AliasesProbe() {
  const aliases = usePathAliases();
  return <div data-testid="aliases">{JSON.stringify(aliases)}</div>;
}

describe('usePathAliases', () => {
  it('reads path_aliases from config (null -> []) via one shared fetch', async () => {
    const configMock = vi.mocked(getConfig);
    configMock.mockClear();
    configMock.mockResolvedValueOnce({
      root_dir: '/library/books/audiobooks',
      path_aliases: null,
    } as unknown as Config);

    render(
      <>
        <AliasesProbe />
        <AliasesProbe />
      </>,
    );

    const results = await screen.findAllByTestId('aliases');
    expect(results).toHaveLength(2);
    for (const el of results) {
      expect(el.textContent).toBe('[]');
    }
    // Two mounted consumers, one shared cached fetch -- not a second network
    // call per consumer.
    expect(configMock).toHaveBeenCalledTimes(1);
  });
});
