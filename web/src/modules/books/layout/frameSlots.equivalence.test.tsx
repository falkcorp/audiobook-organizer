// file: web/src/modules/books/layout/frameSlots.equivalence.test.tsx
// version: 0.1.0
// guid: c4a7e1d9-5b32-4f86-a0c3-9e8d2b6f1a47
// last-edited: 2026-11-21

// F9-02: the top bar and the main layout render and behave the same before and
// after their audiobook imports are cut into slots. Expected values are written
// out by hand from the components as they were before the cut. The operations
// indicator and the user menu are stubbed (each has its own tests); what is
// pinned here is the frame around them: the order, the title, the library
// search, the connection chip, and the banner above the page.

import { describe, it, expect, vi, beforeEach, afterEach } from 'vitest';
import { act, fireEvent, screen, within } from '@testing-library/react';
import { renderWithProviders } from '../../../test/renderWithProviders';
import { TopBar } from '../../../components/layout/TopBar';
import { MainLayout } from '../../../components/layout/MainLayout';
import { useReviewStore } from '../../../stores/useReviewStore';

const navigateSpy = vi.fn();
vi.mock('react-router', async () => {
  const actual = await vi.importActual<typeof import('react-router')>('react-router');
  return { ...actual, useNavigate: () => navigateSpy };
});

vi.mock('../../../components/layout/OperationsIndicator', () => ({
  OperationsIndicator: () => <div data-testid="ops-stub" />,
}));
vi.mock('../../../components/layout/UserMenu', () => ({
  UserMenu: () => <div data-testid="user-stub" />,
}));

const pendingOps = vi.fn();
vi.mock('../../../hooks/usePendingFileOps', () => ({
  usePendingFileOps: (opts: unknown) => pendingOps(opts),
}));

// The shared core event stream, as far as the connection chip reads it: a status feed.
// `state` is the stream's own vocabulary (core/web StreamState), not the chip's.
type StreamState = 'idle' | 'hidden' | 'connecting' | 'open' | 'reconnecting';
type Status = { state: StreamState; attempt: number; streamKey: string };
const connection = vi.hoisted(() => ({
  statusListener: null as null | ((s: Status) => void),
  unsubscribe: undefined as unknown as ReturnType<typeof vi.fn>,
  subscribed: 0,
  current: { state: 'idle', attempt: 0, streamKey: '' } as Status,
}));
vi.mock('../../../services/coreEvents', () => ({
  getEventStream: () => ({
    getStatus: () => connection.current,
    onStatus: (listener: (s: Status) => void) => {
      connection.subscribed += 1;
      connection.statusListener = listener;
      return connection.unsubscribe;
    },
  }),
}));

function pushStatus(state: StreamState, streamKey = '') {
  connection.current = { state, attempt: 0, streamKey };
  act(() => connection.statusListener?.(connection.current));
}

beforeEach(() => {
  navigateSpy.mockReset();
  pendingOps.mockReset();
  connection.unsubscribe = vi.fn();
  connection.statusListener = null;
  connection.subscribed = 0;
  connection.current = { state: 'idle', attempt: 0, streamKey: '' };
  useReviewStore.setState({ count: 0, byKind: {}, items: [], itemsLoading: false, _pollTimer: null });
});

function renderTopBar(at = '/dashboard', onMenuClick = vi.fn()) {
  renderWithProviders(<TopBar onMenuClick={onMenuClick} drawerWidth={240} />, { initialEntries: [at] });
  return onMenuClick;
}

describe('frame slots equivalence: TopBar', () => {
  it('shows the title and sends a click on it to the dashboard', () => {
    renderTopBar('/library');
    fireEvent.click(screen.getByText('Audiobook Organizer'));
    expect(navigateSpy).toHaveBeenCalledExactlyOnceWith('/dashboard');
  });

  it('calls onMenuClick from the drawer button', () => {
    const onMenuClick = renderTopBar();
    fireEvent.click(screen.getByRole('button', { name: 'open drawer' }));
    expect(onMenuClick).toHaveBeenCalledTimes(1);
  });

  it('puts the controls in the same order', () => {
    renderTopBar();
    const order = [
      screen.getByRole('button', { name: 'open drawer' }),
      screen.getByText('Audiobook Organizer'),
      screen.getByRole('textbox', { name: 'Search library' }),
      screen.getByRole('button', { name: 'search library' }),
      screen.getByTestId('ops-stub'),
      screen.getByRole('button', { name: 'settings' }),
      screen.getByRole('button', { name: 'toggle color mode' }),
      screen.getByTestId('user-stub'),
    ];
    for (let i = 1; i < order.length; i++) {
      expect(order[i - 1].compareDocumentPosition(order[i]) & Node.DOCUMENT_POSITION_FOLLOWING).toBeTruthy();
    }
  });

  it('sends the settings button to /settings', () => {
    renderTopBar();
    fireEvent.click(screen.getByRole('button', { name: 'settings' }));
    expect(navigateSpy).toHaveBeenCalledExactlyOnceWith('/settings');
  });

  it('submits a trimmed library search on Enter', () => {
    renderTopBar();
    const box = screen.getByRole('textbox', { name: 'Search library' });
    fireEvent.change(box, { target: { value: '  Year One  ' } });
    fireEvent.keyDown(box, { key: 'Enter' });
    expect(navigateSpy).toHaveBeenCalledExactlyOnceWith({ pathname: '/library', search: '?search=Year+One' });
  });

  it('submits an empty search as a bare /library', () => {
    renderTopBar();
    fireEvent.click(screen.getByRole('button', { name: 'search library' }));
    expect(navigateSpy).toHaveBeenCalledExactlyOnceWith({ pathname: '/library', search: '' });
  });

  it('does not submit on other keys', () => {
    renderTopBar();
    const box = screen.getByRole('textbox', { name: 'Search library' });
    fireEvent.change(box, { target: { value: 'Joker' } });
    fireEvent.keyDown(box, { key: 'a' });
    expect(navigateSpy).not.toHaveBeenCalled();
  });

  it('fills the box from the URL on /library only', () => {
    renderTopBar('/library?search=Killing+Joke');
    expect(screen.getByRole('textbox', { name: 'Search library' })).toHaveValue('Killing Joke');
  });

  it('leaves the box empty on other pages even with a search param', () => {
    renderTopBar('/series?search=Killing+Joke');
    expect(screen.getByRole('textbox', { name: 'Search library' })).toHaveValue('');
  });

  it('shows no connection chip while the connection is up', () => {
    renderTopBar();
    expect(screen.queryByText(/Connection/)).not.toBeInTheDocument();
    pushStatus('open');
    expect(screen.queryByText(/Connection/)).not.toBeInTheDocument();
  });

  it('shows "Connection lost" while the stream is reconnecting', () => {
    renderTopBar();
    pushStatus('reconnecting');
    expect(screen.getByText('Connection lost')).toBeInTheDocument();
  });

  it('says so at once when the bar mounts while the stream is already reconnecting', () => {
    connection.current = { state: 'reconnecting', attempt: 2, streamKey: '' };
    renderTopBar();
    expect(screen.getByText('Connection lost')).toBeInTheDocument();
  });

  it.each(['idle', 'hidden', 'connecting'] as const)(
    'keeps no chip for a stream that is %s: that says nothing about the link',
    (state) => {
      renderTopBar();
      pushStatus(state);
      expect(screen.queryByText(/Connection/)).not.toBeInTheDocument();
    },
  );

  it('ignores a keyed stream: another view owns its status', () => {
    renderTopBar();
    pushStatus('reconnecting', 'book-batman-year-one');
    expect(screen.queryByText(/Connection/)).not.toBeInTheDocument();
  });

  describe('restoration', () => {
    beforeEach(() => vi.useFakeTimers());
    afterEach(() => vi.useRealTimers());

    it('shows "Connection restored" after a loss and hides it 3 seconds later', () => {
      renderTopBar();
      pushStatus('reconnecting');
      pushStatus('open');
      expect(screen.getByText('Connection restored')).toBeInTheDocument();
      act(() => vi.advanceTimersByTime(2999));
      expect(screen.getByText('Connection restored')).toBeInTheDocument();
      act(() => vi.advanceTimersByTime(1));
      expect(screen.queryByText(/Connection/)).not.toBeInTheDocument();
    });

    it('restarts the 3 second window when the connection drops and returns again', () => {
      renderTopBar();
      pushStatus('reconnecting');
      pushStatus('open');
      act(() => vi.advanceTimersByTime(2000));
      pushStatus('reconnecting');
      pushStatus('open');
      act(() => vi.advanceTimersByTime(2000));
      expect(screen.getByText('Connection restored')).toBeInTheDocument();
      act(() => vi.advanceTimersByTime(1000));
      expect(screen.queryByText(/Connection/)).not.toBeInTheDocument();
    });
  });

  it('subscribes once and unsubscribes on unmount', () => {
    const { unmount } = (() => {
      const r = renderWithProviders(<TopBar onMenuClick={vi.fn()} drawerWidth={240} />);
      return r;
    })();
    expect(connection.subscribed).toBe(1);
    expect(connection.unsubscribe).not.toHaveBeenCalled();
    unmount();
    expect(connection.unsubscribe).toHaveBeenCalledTimes(1);
  });
});

function renderLayout(at = '/dashboard') {
  return renderWithProviders(
    <MainLayout>
      <div data-testid="page">Gotham</div>
    </MainLayout>,
    { initialEntries: [at] },
  );
}

describe('frame slots equivalence: MainLayout', () => {
  it('renders the page inside <main>', () => {
    renderLayout();
    expect(within(screen.getByRole('main')).getByTestId('page')).toBeInTheDocument();
  });

  it('shows no review banner at a zero count', () => {
    renderLayout();
    expect(screen.queryByText(/to review/)).not.toBeInTheDocument();
  });

  it.each([
    [1, 'You have 1 item to review'],
    [7, 'You have 7 items to review'],
  ])('shows the review banner for %i above the page', (count, text) => {
    useReviewStore.setState({ count });
    renderLayout();
    const banner = within(screen.getByRole('main')).getByText(text);
    const page = screen.getByTestId('page');
    expect(banner.compareDocumentPosition(page) & Node.DOCUMENT_POSITION_FOLLOWING).toBeTruthy();
  });

  it('sends a click on the review banner to /review', () => {
    useReviewStore.setState({ count: 3 });
    renderLayout();
    fireEvent.click(screen.getByText('You have 3 items to review'));
    expect(navigateSpy).toHaveBeenCalledExactlyOnceWith('/review');
  });

  it('starts the global pending-file-ops poller with toasts on', () => {
    renderLayout();
    expect(pendingOps).toHaveBeenCalled();
    for (const call of pendingOps.mock.calls) {
      expect(call).toEqual([{ fireToasts: true }]);
    }
  });

  it('puts the top bar, the sidebar and the page on screen together', () => {
    renderLayout();
    expect(screen.getByText('Audiobook Organizer')).toBeInTheDocument();
    expect(screen.getByRole('button', { name: 'Dashboard' })).toBeInTheDocument();
    expect(screen.getByRole('button', { name: 'Collapse sidebar' })).toBeInTheDocument();
  });

  it('collapses and expands the sidebar from its toggle', () => {
    renderLayout();
    // The labels render twice while expanded: the always-mounted mobile drawer
    // and the permanent one. Collapsing drops only the permanent drawer's text.
    expect(screen.getAllByText('File Browser')).toHaveLength(2);
    fireEvent.click(screen.getByRole('button', { name: 'Collapse sidebar' }));
    expect(screen.getByRole('button', { name: 'Expand sidebar' })).toBeInTheDocument();
    expect(screen.getAllByText('File Browser')).toHaveLength(1);
    fireEvent.click(screen.getByRole('button', { name: 'Expand sidebar' }));
    expect(screen.getAllByText('File Browser')).toHaveLength(2);
  });

  it('opens the mobile drawer from the top bar button', () => {
    renderLayout();
    // Closed, the mobile drawer is hidden from the accessibility tree and the
    // permanent drawer (with its collapse toggle) is the visible one.
    expect(screen.getByRole('button', { name: 'Collapse sidebar' })).toBeInTheDocument();
    fireEvent.click(screen.getByRole('button', { name: 'open drawer' }));
    // Open, the modal drawer hides the rest of the page and has no toggle.
    expect(screen.queryByRole('button', { name: 'Collapse sidebar' })).not.toBeInTheDocument();
    expect(screen.getByRole('button', { name: 'Dashboard' })).toBeInTheDocument();
  });
});
