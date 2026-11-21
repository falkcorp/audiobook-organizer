// file: web/src/modules/books/pages/usersExtensions.ts
// version: 0.1.0
// guid: e73281ee-1acf-44a2-97ff-487f2056da96
// last-edited: 2026-11-21

// F9-04 slice 4: books' contribution to the Users page (07 §7.11 userPageExtensions). When
// web resolves @mediacore/shell F9-03 types this as UserPageExtensions and the shell lazy-loads it.

import type { UsersPageExtensions } from '../../../components/users/usersExtensions';
import { GuestPhoneKeysPanel } from './GuestPhoneKeysPanel';
import { UserMergesPanel } from './UserMerge';
import { UserMergeRowAction } from './UserMergeRowAction';

export const booksUsersExtensions: UsersPageExtensions = {
  rowActions: [UserMergeRowAction],
  panels: [UserMergesPanel, GuestPhoneKeysPanel],
};
