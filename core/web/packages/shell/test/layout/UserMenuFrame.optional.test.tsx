// file: core/web/packages/shell/test/layout/UserMenuFrame.optional.test.tsx
// version: 0.1.0
// guid: 3b6d0f84-7a21-4c95-b8e3-1f5c9a2d7e40
// last-edited: 2026-11-22

// F9-03 prep: onSaveProfile and onChangePassword are optional; without one the
// frame hides that menu entry (core's client has no such route yet).

import { fireEvent, screen } from '@testing-library/react';
import { describe, expect, it, vi } from 'vitest';
import { UserMenuFrame } from '../../src/layout/UserMenuFrame';
import { renderWithProviders } from './render';

const auth = {
  status: 'authenticated' as const,
  user: { id: 'w1', username: 'bruce', email: 'b@wayne.test' },
  logout: vi.fn(async () => {}),
  refresh: vi.fn(async () => {}),
};

describe('UserMenuFrame optional saves', () => {
  it('hides each entry whose callback is absent, and shows the one given', () => {
    renderWithProviders(<UserMenuFrame auth={auth} onSaveProfile={async () => {}} />);
    fireEvent.click(screen.getByText('bruce'));
    expect(screen.getByText('Edit profile')).toBeInTheDocument();
    expect(screen.queryByText('Change password')).not.toBeInTheDocument();
    expect(screen.getByText('Logout')).toBeInTheDocument();
  });
});
