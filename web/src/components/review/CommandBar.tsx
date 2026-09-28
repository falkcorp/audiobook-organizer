// file: web/src/components/review/CommandBar.tsx
// version: 2.0.1
// guid: 9d3a7f21-5e64-4c08-b73f-2a915c8d0e47
// last-edited: 2026-09-27
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
// SIMPLE FIRST, ADVANCED ON REQUEST (owner, 2026-09-27)
//
// The Dedup menu was nine peer items, each subtitled "library-wide", and the
// owner's verdict was "what is a user supposed to use? it makes no sense." A
// sectioned menu now has two parts:
//
//  - `simple` -- always shown: a one-button "do everything" plus the few
//    commands an ordinary user needs.
//  - `advanced` -- labelled groups of related commands, shown only when the
//    global "Show advanced settings" switch is on (Settings page). When it is
//    off, a one-line hint says where to turn it on, so nothing is hidden
//    without a way to find it.
//
// Every item carries a one-sentence plain-language `description` of what it
// does and when to use it, and the popover is wide enough to read them.
//
// The old "library-wide" subtitle is gone from described items. It existed
// because PLAN.md required the UI to say a menu item starts a corpus-wide job,
// not a per-row one; the owner's point was that when EVERY item says it, it
// says nothing. The descriptions now name what each command acts on ("every
// book", "the books you've ticked"), which carries the same warning in words.
// A flat `commands` menu without descriptions (Queue) keeps the old subtitle.
//
// NOTHING HERE IS SILENTLY DEAD. A command with no route behind it is rendered
// disabled with the reason in a tooltip, never omitted and never a no-op. An
// item that does nothing when clicked is indistinguishable from a bug.

import { useState, type ReactNode } from 'react';
import {
  Box,
  Button,
  Divider,
  Link,
  ListItemText,
  ListSubheader,
  Menu,
  MenuItem,
  Tooltip,
  Typography,
} from '@mui/material';
import ArrowDropDownIcon from '@mui/icons-material/ArrowDropDown';
import { Link as RouterLink } from 'react-router-dom';
import { useAdvancedSettings } from '../../hooks/useAdvancedSettings';

/**
 * Where a command's effect lands.
 *
 * - `library` — a background job over the whole corpus.
 * - `selection` — acts on the rows the reviewer has ticked. Disabled at zero.
 * - `view` — changes only what this screen shows. No server call.
 */
export type CommandScope = 'library' | 'selection' | 'view';

export interface ReviewCommand {
  id: string;
  label: string;
  scope: CommandScope;
  run: () => void | Promise<void>;
  /** One plain-language sentence: what it does and when you would use it. */
  description?: string;
  /** The section's main "do everything" button: rendered emphasised. */
  primary?: boolean;
  /** When set, the item renders disabled and the reason is shown on hover. */
  disabledReason?: string;
  /** Draw a divider above this item (flat menus only). */
  startsGroup?: boolean;
}

/** A labelled run of related commands inside the simple or advanced section. */
export interface CommandGroup {
  id: string;
  /** Group header. Omit for an unlabelled group (e.g. the simple section's first). */
  title?: string;
  commands: ReviewCommand[];
}

export interface CommandMenu {
  id: string;
  label: string;
  /** A flat, unsectioned menu. Used when `simple` is absent. */
  commands?: ReviewCommand[];
  /** Always-visible groups. Presence makes this a sectioned menu. */
  simple?: CommandGroup[];
  /** Groups shown only while the global advanced setting is on. */
  advanced?: CommandGroup[];
}

const SCOPE_NOTE: Record<CommandScope, string | null> = {
  library: 'library-wide',
  selection: null,
  view: null,
};

function CommandItem({ cmd, onPicked }: { cmd: ReviewCommand; onPicked: () => void }) {
  const secondary = cmd.description ?? SCOPE_NOTE[cmd.scope];
  const item = (
    <MenuItem
      disabled={Boolean(cmd.disabledReason)}
      data-testid={`command-${cmd.id}`}
      onClick={() => {
        onPicked();
        void cmd.run();
      }}
      // MenuItem is nowrap by default; descriptions must wrap to be readable.
      sx={{
        whiteSpace: 'normal',
        alignItems: 'flex-start',
        ...(cmd.primary && {
          bgcolor: 'action.selected',
          borderLeft: 3,
          borderColor: 'primary.main',
        }),
      }}
    >
      <ListItemText
        primary={cmd.label}
        secondary={secondary}
        slotProps={{
          primary: { sx: { fontWeight: cmd.primary ? 600 : undefined } },
          secondary: { 'data-testid': `command-${cmd.id}-description` } as object,
        }}
      />
    </MenuItem>
  );

  if (!cmd.disabledReason) return item;
  // A disabled MenuItem does not fire pointer events, so the tooltip needs a
  // wrapper that does -- otherwise the one place that explains WHY it is
  // disabled is unreachable.
  return (
    <Tooltip title={cmd.disabledReason} placement="right">
      <span>{item}</span>
    </Tooltip>
  );
}

function sectionHeader(id: string, text: string, level: 'section' | 'group') {
  return (
    <ListSubheader
      key={id}
      disableSticky
      data-testid={id}
      sx={
        level === 'section'
          ? { lineHeight: 2.5, fontWeight: 700, textTransform: 'uppercase', letterSpacing: 0.5 }
          : { lineHeight: 2, pl: 3, color: 'text.secondary' }
      }
    >
      {text}
    </ListSubheader>
  );
}

function CommandMenuButton({ menu }: { menu: CommandMenu }) {
  const [anchor, setAnchor] = useState<HTMLElement | null>(null);
  const { showAdvanced } = useAdvancedSettings();
  const open = Boolean(anchor);
  const close = () => setAnchor(null);
  const sectioned = Boolean(menu.simple);

  // MUI Menu does not accept Fragments as children, so everything is built as
  // one flat keyed array.
  const children: ReactNode[] = [];
  const pushGroup = (group: CommandGroup, sectionId: string) => {
    if (group.title) {
      children.push(
        sectionHeader(`command-group-${menu.id}-${sectionId}-${group.id}`, group.title, 'group')
      );
    }
    for (const cmd of group.commands) {
      children.push(<CommandItem key={cmd.id} cmd={cmd} onPicked={close} />);
    }
  };

  if (sectioned) {
    children.push(sectionHeader(`command-section-${menu.id}-simple`, 'Simple', 'section'));
    menu.simple!.forEach((g) => pushGroup(g, 'simple'));
    if (menu.advanced && menu.advanced.length > 0) {
      children.push(<Divider key="advanced-divider" />);
      if (showAdvanced) {
        children.push(sectionHeader(`command-section-${menu.id}-advanced`, 'Advanced', 'section'));
        menu.advanced.forEach((g) => pushGroup(g, 'advanced'));
      } else {
        children.push(
          <Box
            key="advanced-hint"
            sx={{ px: 2, py: 1 }}
            data-testid={`command-advanced-hint-${menu.id}`}
          >
            <Typography variant="caption" color="text.secondary">
              More options:{' '}
              <Link component={RouterLink} to="/settings" underline="hover" onClick={close}>
                Settings → Show advanced settings
              </Link>
            </Typography>
          </Box>
        );
      }
    }
  } else {
    for (const cmd of menu.commands ?? []) {
      if (cmd.startsGroup) children.push(<Divider key={`${cmd.id}-divider`} />);
      children.push(<CommandItem key={cmd.id} cmd={cmd} onPicked={close} />);
    }
  }

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
        onClose={close}
        slotProps={{
          list: { 'aria-label': `${menu.label} commands` },
          // Wider than MUI's default so a sentence of description fits on two
          // lines; capped so it never overflows a phone screen.
          paper: sectioned ? { sx: { width: 400, maxWidth: 'calc(100vw - 32px)' } } : undefined,
        }}
      >
        {children}
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
