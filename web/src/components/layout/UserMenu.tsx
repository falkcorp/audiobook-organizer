// file: web/src/components/layout/UserMenu.tsx
// version: 1.0.5
// guid: a1b2c3d4-e5f6-7890-abcd-ef1234567890
// last-edited: 2026-11-07

import { useAuth } from '../../contexts/AuthContext';
import { toShellAuth } from '../../modules/books/layout/legacyAuth';
import * as api from '../../services/api';
import { UserMenuFrame } from './UserMenuFrame';

/** The user menu bound to the legacy auth context and the web API; F9-03 re-points importers to the shell and deletes this file. */
export function UserMenu() {
  const auth = useAuth();
  return (
    <UserMenuFrame
      auth={toShellAuth(auth)}
      onSaveProfile={(email) => api.updateMe({ email })}
      onChangePassword={(current, next) =>
        api.changePassword({ current_password: current, new_password: next })
      }
    />
  );
}
