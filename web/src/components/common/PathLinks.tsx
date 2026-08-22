// file: web/src/components/common/PathLinks.tsx
// version: 1.1.1
// guid: 57b9a402-8f25-433d-926e-9f65d70a364b
// last-edited: 2026-08-22

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
import { Stack, Typography, IconButton, Tooltip } from '@mui/material';
import ContentCopyIcon from '@mui/icons-material/ContentCopy';
import { renderPath, type PathRendering } from '../../utils/pathAliases';
import { usePathVars } from '../../utils/formatPath';
import { useToast } from '../toast/ToastProvider';
import { getConfig, type PathAlias } from '../../services/api';

export interface PathLinksProps {
  path: string;
  aliases: PathAlias[];
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
export function PathLinks({ path, aliases, platform }: PathLinksProps): JSX.Element {
  const vars = usePathVars();
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
          <Tooltip title={`Copy ${r.label} path`}>
            <IconButton
              size="small"
              aria-label={`Copy ${r.label} path`}
              onClick={() => void handleCopy(r)}
            >
              <ContentCopyIcon fontSize="inherit" />
            </IconButton>
          </Tooltip>
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
