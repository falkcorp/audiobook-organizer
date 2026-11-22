// file: core/web/packages/shell/src/layout/UserMenu.tsx
// version: 0.1.0
// guid: 6f1b8c35-2d70-4a96-b4e8-0a9c3d7e5f12
// last-edited: 2026-11-21

import { useContext } from 'react';
import type { ShellClient } from '../api/client';
import { unwrap } from '../api/problems';
import { AuthClientContext, useAuth } from '../auth';
import { UserMenuFrame, type UserMenuFrameProps } from './UserMenuFrame';

type UserMenuProps = Partial<Pick<UserMenuFrameProps, 'onSaveProfile' | 'onChangePassword'>>;

/** The default saves need the AuthProvider's client; a menu rendered without one fails at save, not at render. */
function need(client: ShellClient | null): ShellClient {
  if (!client) throw new Error('UserMenu saves through the AuthProvider client: render it inside <AuthProvider> or pass onSaveProfile and onChangePassword');
  return client;
}

/**
 * The shell's user menu: the frame over the shell's own useAuth. Left to
 * itself it saves through core's routes, PATCH /me (the email) and PUT
 * /me/password ({current, new}); a host that keeps its own profile and
 * password routes passes `onSaveProfile` and `onChangePassword` and they win.
 * A refusal throws the Problem's message, which the frame shows in its dialog.
 */
export function UserMenu({ onSaveProfile, onChangePassword }: UserMenuProps) {
  const auth = useAuth();
  const client = useContext(AuthClientContext);
  return (
    <UserMenuFrame
      auth={auth}
      onSaveProfile={onSaveProfile ?? ((email) => unwrap(need(client).PATCH('/api/core/v1/me', { body: { email } })))}
      onChangePassword={
        onChangePassword ??
        (async (current, next) => {
          await unwrap(need(client).PUT('/api/core/v1/me/password', { body: { current, new: next } }));
        })
      }
    />
  );
}
