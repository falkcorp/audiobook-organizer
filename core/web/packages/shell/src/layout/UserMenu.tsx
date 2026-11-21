// file: core/web/packages/shell/src/layout/UserMenu.tsx
// version: 0.1.0
// guid: 6f1b8c35-2d70-4a96-b4e8-0a9c3d7e5f12
// last-edited: 2026-11-21

import { useAuth } from '../auth';
import { UserMenuFrame, type UserMenuFrameProps } from './UserMenuFrame';

/**
 * The shell's user menu: the frame over the shell's own useAuth. The profile and
 * password saves are the host's: core's client has no route for either yet
 * (api/interim.ts lists the auth routes it does have), so the host passes them in.
 */
export function UserMenu(props: Omit<UserMenuFrameProps, 'auth'>) {
  return <UserMenuFrame auth={useAuth()} {...props} />;
}
