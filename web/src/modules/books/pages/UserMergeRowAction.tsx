// file: web/src/modules/books/pages/UserMergeRowAction.tsx
// version: 0.1.0
// guid: ec19fe5c-e59a-4d07-959a-8499b745953a
// last-edited: 2026-11-21

// F9-04 slice 4: books' "Merge into…" row action on the Users page (07 §7.11 userPageExtensions).
// Merging moves listening data, so the button, who may be merged and who may survive are books'
// rules; the shell page only offers the slot.

import { useState } from 'react';
import { IconButton, Tooltip } from '@mui/material';
import { MergeType as MergeIcon } from '@mui/icons-material';
import type { UserRowActionProps } from '../../../components/users/usersExtensions';
import { isSystemAccount } from '../../../components/users/userAccountParts';
import { UserMergeDialog } from './UserMerge';

export function UserMergeRowAction({ user, users, onDone }: UserRowActionProps) {
  const [open, setOpen] = useState(false);

  if (isSystemAccount(user) || user.status !== 'active' || user.merged_into_user_id) {
    return null;
  }

  return (
    <>
      <Tooltip title="Merge into…">
        <IconButton
          size="small"
          aria-label={`Merge ${user.username} into another account`}
          onClick={() => setOpen(true)}
        >
          <MergeIcon fontSize="small" />
        </IconButton>
      </Tooltip>
      <UserMergeDialog
        key={open ? user.id : 'none'}
        absorbed={open ? user : null}
        candidates={users.filter(
          (c) =>
            c.id !== user.id &&
            !isSystemAccount(c) &&
            c.status === 'active' &&
            !c.merged_into_user_id &&
            (c.roles ?? []).includes('admin')
        )}
        onClose={() => setOpen(false)}
        onStarted={(operationId, absorbed) => {
          setOpen(false);
          onDone(
            `Merging ${absorbed.username} started (operation ${operationId}). It is listed under Account merges when it has begun.`
          );
        }}
      />
    </>
  );
}
