// file: web/src/theme.slot-merge.test.ts
// version: 1.0.0
// guid: 5a91c7e4-2f83-4d16-b0a7-9c3e5d8f241b
// last-edited: 2026-08-20

import { describe, expect, it } from 'vitest';
import resolveProps from '@mui/utils/resolveProps';
import { createAppTheme } from './theme';

// The stuck-modal mitigation in theme.ts lives in MuiDrawer.defaultProps as
// `slotProps.transition.exit === false`. Whether it actually reaches a Drawer
// depends entirely on how MUI merges a theme's `defaultProps.slotProps` with a
// call site's own `slotProps` -- and that is MUI's contract, not ours.
//
// If MUI ever changes that merge from per-slot-recursive to a whole-object
// replace, every Drawer that passes any slotProps of its own would silently
// lose `exit: false` and the full-viewport-backdrop defect would come back on
// exactly those call sites. Nothing else in the suite would notice: the drawer
// still renders, still opens, and still closes in jsdom. The original defect
// only ever reproduced in real chromium under load.
//
// So this pins the third-party behaviour the mitigation depends on. It is
// deliberately not a test of resolveProps for its own sake -- it feeds in the
// real theme defaults and a real call-site shape.
//
// Historical note: theme.ts used to claim "MUI merges defaultProps shallowly,
// so adding [a call-site slotProps] would silently drop `exit: false`". That
// was wrong when written -- verified against both @mui/utils 6.x and 9.x, whose
// resolveProps implementations are identical here. Shallow merging applies to
// `slots`/`components` and to top-level props, not to `slotProps`.

describe('MuiDrawer default slotProps merging', () => {
  const drawerDefaults = createAppTheme('dark').components?.MuiDrawer?.defaultProps ?? {};

  it('still carries the stuck-modal mitigation', () => {
    // Guards the premise of every assertion below: if someone removes the
    // mitigation from theme.ts, fail here rather than passing vacuously.
    expect(drawerDefaults).toHaveProperty('slotProps.transition.exit', false);
  });

  it('keeps exit: false when a call site passes its own paper slotProps', () => {
    // This is the CandidateCompareDrawer shape, which the MUI 9 codemod
    // introduced when it rewrote PaperProps into slotProps.paper.
    const merged = resolveProps(drawerDefaults, {
      slotProps: { paper: { sx: { width: 640 } } },
    }) as { slotProps?: { transition?: { exit?: boolean }; paper?: unknown } };

    expect(merged.slotProps?.transition?.exit).toBe(false);
    // The call site's own slot must survive too, or the drawer loses its width.
    expect(merged.slotProps?.paper).toEqual({ sx: { width: 640 } });
  });

  it('lets a call site deliberately override the mitigation', () => {
    // Not an endorsement -- documenting that the escape hatch exists, so a
    // future stall report can be traced to an intentional override rather than
    // to the merge silently dropping the default.
    const merged = resolveProps(drawerDefaults, {
      slotProps: { transition: { exit: true } },
    }) as { slotProps?: { transition?: { exit?: boolean } } };

    expect(merged.slotProps?.transition?.exit).toBe(true);
  });

  it('applies the mitigation when a call site passes no slotProps at all', () => {
    const merged = resolveProps(drawerDefaults, { open: true }) as {
      slotProps?: { transition?: { exit?: boolean } };
    };

    expect(merged.slotProps?.transition?.exit).toBe(false);
  });
});
