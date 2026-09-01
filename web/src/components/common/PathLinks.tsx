// file: web/src/components/common/PathLinks.tsx
// version: 1.2.0
// guid: 57b9a402-8f25-433d-926e-9f65d70a364b
// last-edited: 2026-09-01

// Renders a book's file location as the several forms a remote client can act
// on: a clickable smb:// link where the client OS registers a handler, a
// Windows W:\ path, and a \\host\share UNC path -- each with its own copy
// button. See docs/design/2026-08-20-dual-path-display.md.
//
// Deliberately not colored info.main and never label-prefixed: CompareSpine
// already renders a blue "iTunes: ..." line, and these derived rows must stay
// visually distinct from it -- on rows with a corrupt stored path the two
// will disagree, and that disagreement is a signal worth seeing (spec
// Decision 3).

import { useEffect, useState, type JSX } from 'react';
import { Stack, Typography, IconButton } from '@mui/material';
import ContentCopyIcon from '@mui/icons-material/ContentCopy';
import { renderPath, type PathRendering } from '../../utils/pathAliases';
import type { PathVar } from '../../utils/formatPath';
import { useToast } from '../toast/ToastProvider';
import { getConfig, type PathAlias } from '../../services/api';

export interface PathLinksProps {
  path: string;
  aliases: PathAlias[];
  /**
   * The abbreviation vars, from usePathVars(). A PROP, not a hook call inside
   * this component, and that is load-bearing rather than stylistic.
   *
   * PathLinks renders twice per dupes row. Calling usePathVars() internally
   * meant 200 independent useState+useEffect pairs at the 100-row page cap,
   * every one of them resolving the SAME shared config promise in the same
   * microtask drain and calling setVars -- i.e. a second full render pass over
   * 200 subtrees immediately after mount. Ablating that hook was measured at
   * 117 ms of 763 ms (15%) of the lane's blocked main-thread time.
   *
   * `aliases` was already threaded this way for the same reason; DupesSpine's
   * own comment claimed the row stayed pure and "doesn't each re-fetch config
   * on its own", which was true of aliases and false of vars. Now it is true
   * of both.
   */
  vars: PathVar[];
  /** Forces the client-OS detection `renderPath` otherwise does itself --
   *  lets tests pin a platform instead of depending on the jsdom UA. */
  platform?: string;
}

/**
 * PathLinks renders one line per available path form (POSIX/Windows/UNC),
 * each with a monospace label and its own copy button. The POSIX line
 * becomes a clickable smb:// anchor only when the client OS is known to
 * register a handler for the scheme (see hasSchemeHandler); otherwise every
 * line is plain text. The UNC line is muted -- it's the reliable form for
 * Windows clients but the secondary one, after the shorter Windows path.
 */
export function PathLinks({ path, aliases, vars, platform }: PathLinksProps): JSX.Element {
  const { toast } = useToast();
  const renderings = renderPath(path, aliases, vars, platform);

  const handleCopy = async (r: PathRendering) => {
    try {
      // Always copyText, the full literal path -- never `display`, which may
      // be abbreviated to $(books)/... and would be useless pasted elsewhere.
      await navigator.clipboard.writeText(r.copyText);
      toast(`Copied ${r.label} path`, 'success');
    } catch (err) {
      // Copy is the primary affordance on Windows (no smb:// anchor there --
      // see hasSchemeHandler), so a silent failure here is not acceptable.
      toast(err instanceof Error ? `Copy failed: ${err.message}` : 'Copy failed', 'error');
    }
  };

  return (
    <Stack spacing={0.25}>
      {renderings.map((r) => (
        <Stack
          key={r.key}
          direction="row"
          spacing={0.5}
          sx={{ alignItems: 'center', ...(r.key === 'unc' ? { opacity: 0.6 } : {}) }}
        >
          <Typography
            variant="caption"
            component={r.href ? 'a' : 'span'}
            href={r.href ?? undefined}
            title={r.copyText}
            sx={{
              fontFamily: 'monospace',
              color: 'inherit',
              textDecoration: r.href ? 'underline' : 'none',
              wordBreak: 'break-all',
            }}
          >
            {r.display}
          </Typography>
          {/*
            A plain `title`, not an MUI <Tooltip>. MEASURED: an MUI Tooltip is
            the single most expensive thing on a dupes row -- an ablation at
            the 100-row page cap that removed ONLY the Tooltip wrappers, leaving
            an identical 58 DOM nodes per row, cut blocked main-thread time from
            763 ms to 421 ms (45%). Per instance that is ~85 ms/100 rows against
            ~14 ms for a Button and ~3 ms for a Chip, because a Tooltip carries
            a useId, five useTimeout instances, useControlled, useIsFocusVisible
            and a cloneElement over its child -- all of it dead weight for the
            99 rows nobody points at.
            This particular one lost the least by going native: its text was
            BYTE-IDENTICAL to the aria-label already on this button, so
            assistive tech is unaffected, and the sibling Typography above
            already renders its own hint through a plain `title` attribute.
            What changes is the hover affordance: browser-native styling and
            delay rather than MUI's.
          */}
          <IconButton
            size="small"
            aria-label={`Copy ${r.label} path`}
            title={`Copy ${r.label} path`}
            onClick={() => void handleCopy(r)}
          >
            <ContentCopyIcon fontSize="inherit" />
          </IconButton>
        </Stack>
      ))}
    </Stack>
  );
}

// Single shared fetch of the config so every consumer of usePathAliases
// reuses one request rather than each refetching /config -- mirrors the
// cachedVarsPromise pattern in formatPath.ts (kept separate because that
// file mirrors internal/pathutil/abbreviate.go and must not gain an
// unrelated concern).
let cachedAliasesPromise: Promise<PathAlias[]> | null = null;

function loadPathAliases(): Promise<PathAlias[]> {
  if (!cachedAliasesPromise) {
    cachedAliasesPromise = getConfig()
      // path_aliases has no `omitempty` on the Go side, so it can arrive as
      // JSON null -- `?? []` is required, not defensive decoration.
      .then((cfg) => cfg.path_aliases ?? [])
      .catch(() => {
        // On failure, don't poison the cache -- allow a later retry.
        cachedAliasesPromise = null;
        return [];
      });
  }
  return cachedAliasesPromise;
}

/**
 * usePathAliases returns the configured path aliases, loaded once from
 * config and shared across all callers.
 */
export function usePathAliases(): PathAlias[] {
  const [aliases, setAliases] = useState<PathAlias[]>([]);
  useEffect(() => {
    let alive = true;
    void loadPathAliases().then((a) => {
      if (alive) setAliases(a);
    });
    return () => {
      alive = false;
    };
  }, []);
  return aliases;
}

/**
 * Test-only: clears the module-scope config-fetch cache so the next
 * usePathAliases()/loadPathAliases() call re-fetches. Call from
 * beforeEach/afterEach in any test file that needs a fresh or per-test alias
 * set -- without it, the first test in a file seeds the promise for every
 * later test in that file (and for any file sharing the module instance in the
 * same Vitest worker).
 *
 * Safe to call while a fetch is still in flight: nulling the `let` only means
 * that promise's result stops being cached, and the in-flight consumer's
 * `.then` still fires against its own promise reference.
 */
export function __resetPathAliasesCacheForTests(): void {
  cachedAliasesPromise = null;
}
