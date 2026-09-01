// file: web/src/components/common/PathLinks.test.tsx
// version: 1.5.0
// guid: 19ec3b3a-a184-4122-953f-32ebd321116c
// last-edited: 2026-09-01

import { describe, it, expect, vi, beforeEach } from 'vitest';
import { render, screen, waitFor } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { PathLinks, usePathAliases, __resetPathAliasesCacheForTests } from './PathLinks';
import { __resetPathVarsCacheForTests, derivePathVars } from '../../utils/formatPath';
import { getConfig, type Config, type PathAlias } from '../../services/api';

// A real root_dir so the abbreviation vars below turn the posix display into
// $(books)/... -- without this, display and copyText are identical strings and
// the "copies the literal path, not the abbreviated display" test below cannot
// fail even if the component copies the wrong field.
//
// The vars now arrive as a PROP (they used to come from a usePathVars() call
// inside PathLinks; see the vars prop's own comment for why that moved). That
// makes these tests synchronous, but it also means they no longer prove the
// vars reach a row from a real spine -- DupesSpine.test.tsx owns that wiring
// assertion.
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
const VARS = derivePathVars('/library/books/audiobooks');

beforeEach(() => {
  // First, before any mock setup: drop the module-scope config-fetch promises
  // both usePathAliases and usePathVars memoize, so each test below starts
  // from a fresh fetch instead of inheriting whatever the first test in this
  // file happened to seed.
  __resetPathAliasesCacheForTests();
  __resetPathVarsCacheForTests();
  toastSpy.mockClear();
  Object.assign(navigator, { clipboard: { writeText: vi.fn().mockResolvedValue(undefined) } });
});

describe('PathLinks', () => {
  it('renders an anchor for the posix line on a handler platform', () => {
    render(<PathLinks path={P} aliases={ALIASES} vars={VARS} platform="macOS" />);
    expect(screen.getByRole('link')).toHaveAttribute(
      'href',
      'smb://host/books/Author/Title/x.m4b',
    );
  });

  it('renders no anchor at all on Windows', () => {
    render(<PathLinks path={P} aliases={ALIASES} vars={VARS} platform="Win32" />);
    expect(screen.queryByRole('link')).toBeNull();
    expect(screen.getByText(/W:\\Author\\Title\\x\.m4b/)).toBeInTheDocument();
  });

  it('copies the literal path, not the abbreviated display', async () => {
    render(<PathLinks path={P} aliases={ALIASES} vars={VARS} platform="macOS" />);
    // Wait for the mocked config fetch to resolve so the posix display has
    // actually abbreviated to $(books)/... -- otherwise display === copyText
    // before hydration and a component that copies `display` would pass too.
    await screen.findByText(/\$\(books\)/);
    await userEvent.click(screen.getByRole('button', { name: /copy linux path/i }));
    expect(navigator.clipboard.writeText).toHaveBeenCalledWith(P);
  });

  it('exposes the literal path via title even when the display is abbreviated', async () => {
    // Forces display !== copyText via the mocked root_dir above -- otherwise
    // this assertion would pass vacuously even if `title` were wired to
    // `display` instead of `copyText`.
    render(<PathLinks path={P} aliases={ALIASES} vars={VARS} platform="macOS" />);
    const posixText = await screen.findByText(/\$\(books\)/);
    expect(posixText).not.toHaveTextContent(P);
    expect(posixText).toHaveAttribute('title', P);
  });

  it('keeps a hover hint on the copy button now that the MUI Tooltip is gone', () => {
    // The <Tooltip> that wrapped this button was replaced by a native `title`
    // (see the measurement note in PathLinks.tsx). Every OTHER query in this
    // file finds the button by role+name, which resolves from `aria-label` and
    // passes identically whether the `title` landed or not -- so without this
    // assertion the hover hint could vanish with all 323 tests still green.
    render(<PathLinks path={P} aliases={ALIASES} vars={VARS} platform="macOS" />);
    expect(screen.getByRole('button', { name: 'Copy Linux path' })).toHaveAttribute(
      'title',
      'Copy Linux path',
    );
  });

  it('gives every rendering its own copy button', () => {
    render(<PathLinks path={P} aliases={ALIASES} vars={VARS} platform="macOS" />);
    expect(screen.getAllByRole('button', { name: /copy/i })).toHaveLength(3);
  });

  it('renders a single line when no alias matches', () => {
    render(<PathLinks path="/elsewhere/x.m4b" aliases={ALIASES} vars={VARS} platform="macOS" />);
    expect(screen.getAllByRole('button', { name: /copy/i })).toHaveLength(1);
    expect(screen.queryByRole('link')).toBeNull();
  });

  it('surfaces a failure toast when the clipboard write rejects, not a silent no-op', async () => {
    Object.assign(navigator, {
      clipboard: { writeText: vi.fn().mockRejectedValue(new Error('denied')) },
    });
    render(<PathLinks path={P} aliases={ALIASES} vars={VARS} platform="macOS" />);
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

// Two alias sets that share no root, so a probe rendering one cannot be
// mistaken for a probe rendering the other.
const ALIASES_A: PathAlias[] = [
  { root: '/vol/alpha', windows: 'A:', unc: '\\\\host\\alpha', smb_url: 'smb://host/alpha' },
];
const ALIASES_B: PathAlias[] = [
  { root: '/vol/beta', windows: 'B:', unc: '\\\\host\\beta', smb_url: 'smb://host/beta' },
];

// A cross-test pair, deliberately not a reset-then-re-render inside one test
// body. A single test would only prove the exported function is callable; this
// pair proves the beforeEach call above is load-bearing. The second test can
// see its own alias set ONLY because beforeEach cleared the module-scope
// promise the first test left cached -- delete __resetPathAliasesCacheForTests()
// from the beforeEach and the second test fails with '/vol/alpha' where it
// expects '/vol/beta'.
describe('__resetPathAliasesCacheForTests', () => {
  it('lets a test seed its own alias set (first of the pair)', async () => {
    vi.mocked(getConfig).mockResolvedValueOnce({
      root_dir: '/library/books/audiobooks',
      path_aliases: ALIASES_A,
    } as unknown as Config);

    render(<AliasesProbe />);

    await waitFor(() => {
      expect(screen.getByTestId('aliases').textContent).toContain('/vol/alpha');
    });
  });

  it('lets the next test seed a different one, not the previous test’s cache', async () => {
    vi.mocked(getConfig).mockResolvedValueOnce({
      root_dir: '/library/books/audiobooks',
      path_aliases: ALIASES_B,
    } as unknown as Config);

    render(<AliasesProbe />);

    await waitFor(() => {
      expect(screen.getByTestId('aliases').textContent).toContain('/vol/beta');
    });
    expect(screen.getByTestId('aliases').textContent).not.toContain('/vol/alpha');
  });
});
