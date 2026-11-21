// file: web/src/components/layout/TopBarFrame.test.tsx
// version: 0.1.0
// guid: 2b6d9e48-3f17-4a05-9c8e-d1a7f4b0c653
// last-edited: 2026-11-21

// TopBarFrame takes its title, slots and connection feed as props and names no
// audiobook route or service, so this test feeds it Gotham ones of its own. It
// moves into core/web/packages/shell/src/layout/ with the frame.

import { describe, it, expect, vi, afterEach, beforeEach } from 'vitest';
import { act, fireEvent, screen } from '@testing-library/react';
import { renderWithProviders } from '../../test/renderWithProviders';
import { TopBarFrame, type ConnectionFeed, type ConnectionState } from './TopBarFrame';

function feed() {
  let push: (s: ConnectionState) => void = () => undefined;
  const unsubscribe = vi.fn();
  const subscribe = vi.fn((on: (s: ConnectionState) => void) => {
    push = on;
    return unsubscribe;
  });
  const connection: ConnectionFeed = { subscribe };
  return { connection, subscribe, unsubscribe, push: (s: ConnectionState) => act(() => push(s)) };
}

describe('TopBarFrame', () => {
  beforeEach(() => vi.useFakeTimers());
  afterEach(() => vi.useRealTimers());

  it('renders the title and the three slots in order, the toggle between actions and account', () => {
    renderWithProviders(
      <TopBarFrame
        onMenuClick={vi.fn()}
        drawerWidth={240}
        title="Wayne Tower"
        center={<div data-testid="center" />}
        actions={<div data-testid="actions" />}
        account={<div data-testid="account" />}
      />,
    );
    const order = [
      screen.getByText('Wayne Tower'),
      screen.getByTestId('center'),
      screen.getByTestId('actions'),
      screen.getByRole('button', { name: 'toggle color mode' }),
      screen.getByTestId('account'),
    ];
    for (let i = 1; i < order.length; i++) {
      expect(order[i - 1].compareDocumentPosition(order[i]) & Node.DOCUMENT_POSITION_FOLLOWING).toBeTruthy();
    }
  });

  it('calls onTitleClick from the title and onMenuClick from the drawer button', () => {
    const onTitleClick = vi.fn();
    const onMenuClick = vi.fn();
    renderWithProviders(
      <TopBarFrame onMenuClick={onMenuClick} drawerWidth={240} title="Wayne Tower" onTitleClick={onTitleClick} />,
    );
    fireEvent.click(screen.getByText('Wayne Tower'));
    fireEvent.click(screen.getByRole('button', { name: 'open drawer' }));
    expect(onTitleClick).toHaveBeenCalledTimes(1);
    expect(onMenuClick).toHaveBeenCalledTimes(1);
  });

  it('renders without a connection feed and shows no chip', () => {
    renderWithProviders(<TopBarFrame onMenuClick={vi.fn()} drawerWidth={240} title="Wayne Tower" />);
    expect(screen.queryByText(/Connection/)).not.toBeInTheDocument();
  });

  it('subscribes to the feed once, and unsubscribes on unmount', () => {
    const f = feed();
    const { rerender, unmount } = renderWithProviders(
      <TopBarFrame onMenuClick={vi.fn()} drawerWidth={240} title="A" connection={f.connection} />,
    );
    rerender(<TopBarFrame onMenuClick={vi.fn()} drawerWidth={56} title="B" connection={f.connection} />);
    expect(f.subscribe).toHaveBeenCalledTimes(1);
    unmount();
    expect(f.unsubscribe).toHaveBeenCalledTimes(1);
  });

  it('shows lost on reconnecting, then restored for 3 seconds on open', () => {
    const f = feed();
    renderWithProviders(
      <TopBarFrame onMenuClick={vi.fn()} drawerWidth={240} title="Wayne Tower" connection={f.connection} />,
    );
    f.push('reconnecting');
    expect(screen.getByText('Connection lost')).toBeInTheDocument();
    f.push('open');
    expect(screen.getByText('Connection restored')).toBeInTheDocument();
    act(() => vi.advanceTimersByTime(3000));
    expect(screen.queryByText(/Connection/)).not.toBeInTheDocument();
  });

  it('does not set state after unmount when the restore timer is pending', () => {
    const f = feed();
    const spy = vi.spyOn(console, 'error').mockImplementation(() => undefined);
    const { unmount } = renderWithProviders(
      <TopBarFrame onMenuClick={vi.fn()} drawerWidth={240} title="Wayne Tower" connection={f.connection} />,
    );
    f.push('error');
    f.push('open');
    unmount();
    act(() => vi.advanceTimersByTime(5000));
    expect(spy).not.toHaveBeenCalled();
    spy.mockRestore();
  });
});
