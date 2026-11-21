// file: web/src/components/users/usersExtensions.ts
// version: 0.1.0
// guid: 0ebbc0e4-fde0-40f5-9309-d9a0e67045ee
// last-edited: 2026-11-21

// F9-04 slice 4: the slots the shell's Users page offers a module (07 §7.11 userPageExtensions).
// ABO's local, richer twin of the shell's UserPageExtensions: see seams.d/F9-04-s4-users.md for
// the gaps against the typed shape that F9-03 must close.

import type { ComponentType } from 'react';
import type { MergeableUser } from './userAccountParts';

export interface UserRowActionProps {
  /** The row the action belongs to. */
  user: MergeableUser;
  /** The whole loaded list, for actions that pick a second account. */
  users: MergeableUser[];
  /** The action changed something: the page reloads; a message is shown as the page notice. */
  onDone: (message?: string) => void;
}

export interface UserPanelProps {
  users: MergeableUser[];
  /** Bumped whenever a row action reports done, so a panel that lists its records reloads. */
  refreshKey: number;
  /** Show a message as the page notice and reload the list. */
  onNotice: (message: string) => void;
}

export interface UsersPageExtensions {
  /** Rendered in the actions cell of each row, after the sign-ins button. */
  rowActions?: readonly ComponentType<UserRowActionProps>[];
  /** Rendered below the invitations, in order. */
  panels?: readonly ComponentType<UserPanelProps>[];
}
