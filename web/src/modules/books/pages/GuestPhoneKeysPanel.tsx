// file: web/src/modules/books/pages/GuestPhoneKeysPanel.tsx
// version: 0.1.0
// guid: a6f69d71-75f7-4aa1-893c-b008177f6c8a
// last-edited: 2026-11-21

// F9-04 slice 4: books' phone-app keys panel as a Users page panel (07 §7.11 userPageExtensions).
// It reloads whenever the page reloads its user list, as the page's own render of it always did.

import type { UserPanelProps } from '../../../components/users/usersExtensions';
import GuestPhoneKeys from './GuestPhoneKeys';

export function GuestPhoneKeysPanel({ users }: UserPanelProps) {
  return <GuestPhoneKeys refreshToken={users} />;
}
