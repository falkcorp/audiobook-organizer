// file: core/web/packages/ui-kit/src/components/AnnouncementBanner.test.tsx
// version: 0.1.0
// guid: 10e32e1e-679d-4546-89f1-4fdba3ea62c8
// last-edited: 2026-11-21

import '@testing-library/jest-dom/vitest';
import { fireEvent, render, screen } from '@testing-library/react';
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import { AnnouncementBanner, type Announcement } from './AnnouncementBanner';

const KEY = 'test-app.announcements.dismissed';
const NOTICE: Announcement = { id: 'a1', severity: 'info', message: 'Gotham maintenance tonight' };

beforeEach(() => localStorage.clear());
afterEach(() => vi.restoreAllMocks());

describe('AnnouncementBanner', () => {
  it('shows each announcement with the title its severity calls for', () => {
    render(
      <AnnouncementBanner
        storageKey={KEY}
        announcements={[
          NOTICE,
          { id: 'a2', severity: 'warning', message: 'Penguin at the docks' },
          { id: 'a3', severity: 'error', message: 'Joker has the keys' },
        ]}
      />,
    );
    expect(screen.getByText('Info')).toBeInTheDocument();
    expect(screen.getByText('Attention')).toBeInTheDocument();
    expect(screen.getByText('Action Required')).toBeInTheDocument();
    expect(screen.getByText('Joker has the keys')).toBeInTheDocument();
  });

  it('renders nothing for an empty list', () => {
    const { container } = render(<AnnouncementBanner storageKey={KEY} announcements={[]} />);
    expect(container).toBeEmptyDOMElement();
  });

  it('dismisses one announcement, remembers it under the given key, and keeps the others', () => {
    const two = [NOTICE, { id: 'a2', severity: 'warning' as const, message: 'Penguin at the docks' }];
    const { unmount } = render(<AnnouncementBanner storageKey={KEY} announcements={two} />);
    fireEvent.click(screen.getAllByLabelText('Dismiss')[0]);
    expect(screen.queryByText('Gotham maintenance tonight')).not.toBeInTheDocument();
    expect(screen.getByText('Penguin at the docks')).toBeInTheDocument();
    expect(JSON.parse(localStorage.getItem(KEY)!)).toEqual(['a1']);
    // Only the given key is written.
    expect(localStorage.length).toBe(1);
    unmount();
    // A fresh mount reads it back.
    render(<AnnouncementBanner storageKey={KEY} announcements={two} />);
    expect(screen.queryByText('Gotham maintenance tonight')).not.toBeInTheDocument();
  });

  it('keeps a dismissal another tab stored meanwhile', () => {
    render(<AnnouncementBanner storageKey={KEY} announcements={[NOTICE, { id: 'a2', severity: 'info', message: 'Second' }]} />);
    localStorage.setItem(KEY, JSON.stringify(['elsewhere']));
    fireEvent.click(screen.getAllByLabelText('Dismiss')[0]);
    expect(JSON.parse(localStorage.getItem(KEY)!).sort()).toEqual(['a1', 'elsewhere']);
  });

  it('starts empty when the stored value is corrupt or not a list of ids', () => {
    localStorage.setItem(KEY, '{not json');
    const { unmount } = render(<AnnouncementBanner storageKey={KEY} announcements={[NOTICE]} />);
    expect(screen.getByText('Gotham maintenance tonight')).toBeInTheDocument();
    unmount();
    localStorage.setItem(KEY, JSON.stringify({ a1: true }));
    render(<AnnouncementBanner storageKey={KEY} announcements={[NOTICE]} />);
    expect(screen.getByText('Gotham maintenance tonight')).toBeInTheDocument();
  });

  it('still hides the banner for the session when storage refuses the write', () => {
    vi.spyOn(Storage.prototype, 'setItem').mockImplementation(() => {
      throw new DOMException('full', 'QuotaExceededError');
    });
    render(<AnnouncementBanner storageKey={KEY} announcements={[NOTICE]} />);
    fireEvent.click(screen.getByLabelText('Dismiss'));
    expect(screen.queryByText('Gotham maintenance tonight')).not.toBeInTheDocument();
  });

  it('navigates through onNavigate on a click, but not when dismissing', () => {
    const onNavigate = vi.fn();
    render(<AnnouncementBanner storageKey={KEY} onNavigate={onNavigate} announcements={[{ ...NOTICE, link: '/batcave' }]} />);
    fireEvent.click(screen.getByText('Gotham maintenance tonight'));
    expect(onNavigate).toHaveBeenCalledWith('/batcave');
    onNavigate.mockClear();
    fireEvent.click(screen.getByLabelText('Dismiss'));
    expect(onNavigate).not.toHaveBeenCalled();
  });

  it('does nothing on a click when the announcement has no link, or there is no onNavigate', () => {
    const onNavigate = vi.fn();
    const { rerender } = render(<AnnouncementBanner storageKey={KEY} onNavigate={onNavigate} announcements={[NOTICE]} />);
    fireEvent.click(screen.getByText('Gotham maintenance tonight'));
    expect(onNavigate).not.toHaveBeenCalled();
    rerender(<AnnouncementBanner storageKey={KEY} announcements={[{ ...NOTICE, link: '/batcave' }]} />);
    fireEvent.click(screen.getByText('Gotham maintenance tonight'));
    expect(onNavigate).not.toHaveBeenCalled();
  });
});
