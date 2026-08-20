// file: web/src/components/review/CommandBar.tsx
// version: 1.0.0
// guid: 9d3a7f21-5e64-4c08-b73f-2a915c8d0e47
// last-edited: 2026-08-20
//
// The three command menus: Dedup, Metadata, Queue.
//
// WHY COMMANDS ARE NOT `ReviewAction`s
//
// `reviewActions.ts` models what a reviewer does to a ROW -- apply this match,
// dismiss this candidate, merge these two books. Every one carries an id and is
// answerable by the lane's dispatcher. The command bar is a different kind of
// thing: "Find duplicates", "Rescore", "Purge stale" operate on the corpus and
// start background jobs. Folding them into the same union would give half its
// members a meaningless `id` and force every `switch` over row actions to
// handle commands it can never receive.
//
// SCOPE IS PART OF THE LABEL, NOT A DETAIL
//
// PLAN.md is explicit about this: most of these routes are library-wide, not
// per-candidate, and the UI has to say so. A button that reads "Run AcoustID
// scan" while sitting next to one selected book reads as "scan this book" --
// and silently starting a library-wide job from a row-level control is the
// wrong kind of convenience. `scope: 'library'` renders the qualifier.
//
// NOTHING HERE IS SILENTLY DEAD. A command with no route behind it is rendered
// disabled with the reason in a tooltip, never omitted and never a no-op. An
// item that does nothing when clicked is indistinguishable from a bug.

import { useState, type ReactNode } from 'react';
import { Box, Button, Divider, ListItemText, Menu, MenuItem, Tooltip } from '@mui/material';
import ArrowDropDownIcon from '@mui/icons-material/ArrowDropDown';

/**
 * Where a command's effect lands.
 *
 * - `library` — a background job over the whole corpus. Labelled as such.
 * - `selection` — acts on the rows the reviewer has ticked. Disabled at zero.
 * - `view` — changes only what this screen shows. No server call.
 */
export type CommandScope = 'library' | 'selection' | 'view';

export interface ReviewCommand {
  id: string;
  label: string;
  scope: CommandScope;
  run: () => void | Promise<void>;
  /** When set, the item renders disabled and the reason is shown on hover. */
  disabledReason?: string;
  /** Draw a divider above this item -- PLAN.md's menu groupings. */
  startsGroup?: boolean;
}

export interface CommandMenu {
  id: string;
  label: string;
  commands: ReviewCommand[];
}

const SCOPE_NOTE: Record<CommandScope, string | null> = {
  // Deliberately not "(all books)" -- the point is that it starts a job, not
  // that it has a wide filter.
  library: 'library-wide',
  selection: null,
  view: null,
};

function CommandMenuButton({ menu }: { menu: CommandMenu }) {
  const [anchor, setAnchor] = useState<HTMLElement | null>(null);
  const open = Boolean(anchor);

  return (
    <>
      <Button
        size="small"
        color="inherit"
        endIcon={<ArrowDropDownIcon />}
        onClick={(e) => setAnchor(e.currentTarget)}
        aria-haspopup="menu"
        aria-expanded={open}
        aria-controls={open ? `command-menu-${menu.id}` : undefined}
        data-testid={`command-menu-${menu.id}`}
      >
        {menu.label}
      </Button>
      <Menu
        id={`command-menu-${menu.id}`}
        anchorEl={anchor}
        open={open}
        onClose={() => setAnchor(null)}
        slotProps={{ list: { 'aria-label': `${menu.label} commands` } }}
      >
        {menu.commands.map((cmd) => {
          const note = SCOPE_NOTE[cmd.scope];
          const item = (
            <MenuItem
              key={cmd.id}
              disabled={Boolean(cmd.disabledReason)}
              data-testid={`command-${cmd.id}`}
              onClick={() => {
                setAnchor(null);
                void cmd.run();
              }}
            >
              <ListItemText primary={cmd.label} secondary={note} />
            </MenuItem>
          );

          return (
            <Box key={cmd.id} component="span" sx={{ display: 'contents' }}>
              {cmd.startsGroup && <Divider />}
              {cmd.disabledReason ? (
                // A disabled MenuItem does not fire pointer events, so the
                // tooltip needs a wrapper that does -- otherwise the one place
                // that explains WHY it is disabled is unreachable.
                <Tooltip title={cmd.disabledReason} placement="right">
                  <span>{item}</span>
                </Tooltip>
              ) : (
                item
              )}
            </Box>
          );
        })}
      </Menu>
    </>
  );
}

export function CommandBar({ menus, children }: { menus: CommandMenu[]; children?: ReactNode }) {
  return (
    <Box
      data-testid="command-bar"
      sx={{ display: 'flex', alignItems: 'center', gap: 0.5, flexWrap: 'wrap' }}
    >
      {menus.map((menu) => (
        <CommandMenuButton key={menu.id} menu={menu} />
      ))}
      {children}
    </Box>
  );
}
