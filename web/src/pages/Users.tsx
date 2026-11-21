// file: web/src/pages/Users.tsx
// version: 1.10.1
// guid: 4d2e3f1a-5b6c-4a70-b8c5-3d7e0f1b9a99
// last-edited: 2026-11-07

// ABO's binding of the split Users page (F9-04 slice 4): the shell's users page with books'
// account-merge and phone-app-key extensions. F9-03 deletes this file when the shell mounts the
// page and lazy-loads the module's extensions.

import { UsersPage } from '../components/users/UsersPage';
import { booksUsersExtensions } from '../modules/books/pages/usersExtensions';

export default function Users() {
  return <UsersPage extensions={booksUsersExtensions} />;
}
