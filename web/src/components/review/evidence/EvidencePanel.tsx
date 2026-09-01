// file: web/src/components/review/evidence/EvidencePanel.tsx
// version: 1.2.0
// guid: c07f4b91-8d23-4e56-a1b8-5f2c9d0e3a74
// last-edited: 2026-08-31
//
// The shared "why did it conclude that" panel, promoted out of the dedup lane
// so all three review lanes explain themselves the same way.
//
// It renders whichever of the three evidence kinds it is handed, and the
// rendering is chosen by the ARITHMETIC, not by the lane -- see ./types.ts. The
// short version: a stacked share bar asserts that its parts sum to the whole,
// which is true of a weighted sum and false of a multiplicative pipeline, so
// metadata gets a waterfall and regroup (which has no score at all) gets chips.

import { Box, Chip, Stack, Tooltip, Typography } from '@mui/material';
import type { Theme } from '@mui/material/styles';
import type {
  Evidence,
  FactsEvidence,
  WaterfallEvidence,
  WaterfallStep,
  WeightedEvidence,
  WeightedSignal,
} from './types';
import { incompleteReason, recomposeWaterfall, waterfallIsConsistent } from './types';

/**
 * Zebra striping for a score row, by index.
 *
 * These rows are unusually wide: the label sits hard left and its numbers hard
 * right, with a progress bar between them. At full panel width the eye has to
 * cross several hundred pixels of empty space to pair a label with its value,
 * and adjacent rows are the easiest thing in the world to slip between.
 * Alternating bands give it a rail to follow.
 *
 * The stripe is theme.palette.action.hover rather than a literal colour because
 * MUI defines it for both light and dark themes. A hardcoded rgba would have to
 * be written twice and could drift; this cannot.
 *
 * Callers must also drop the parent Stack's `spacing`: a gap between striped
 * rows breaks the band into floating chips and defeats the point.
 */
const zebraRowSx = (index: number) => (theme: Theme) => ({
  alignItems: 'center',
  px: 1,
  py: 0.5,
  borderRadius: 0.5,
  backgroundColor: index % 2 === 1 ? theme.palette.action.hover : 'transparent',
});

// Human-friendly labels for dedup signal kinds. An unknown kind falls through
// to its raw value rather than rendering blank -- a signal we cannot name is
// still evidence, and hiding it would understate the case.
const SIGNAL_LABELS: Record<string, string> = {
  exact_file: 'Exact file hash',
  exact_acoustid: 'Exact AcoustID',
  isbn_asin: 'ISBN/ASIN',
  lsh_acoustid: 'LSH AcoustID',
  embedding_high: 'Embedding (high)',
  metadata_hash: 'Metadata hash',
  metadata_fuzzy: 'Metadata fuzzy',
  embedding_med: 'Embedding (medium)',
  duration: 'Duration match',
  folder_path: 'Folder path',
};

/**
 * Signal colours come from the theme's categorical palette, which is defined
 * per colour scheme. They used to be hardcoded hexes tuned for a white
 * background; several of them were effectively invisible on the dark paper,
 * which matters because these hues carry meaning -- an unreadable segment is
 * the panel failing to say which signal decided the verdict.
 */
function signalColor(theme: Theme, kind: string): string {
  const palette = theme.vars.palette.signal;
  return (palette as unknown as Record<string, string>)[kind] ?? palette.unknown;
}

/**
 * Format a number that arrived over the wire and may not be one.
 *
 * Steps come from JSON, so `operand` and `running` are only numbers if the
 * backend still calls them that. When a tag is renamed or dropped they arrive
 * as `undefined`, and `undefined.toFixed(2)` throws -- which unmounts the whole
 * review screen over one malformed row. A step we cannot read is a real thing
 * to show the reviewer, not a reason to lose the other four: render it as
 * unreadable and let the "breakdown incomplete" chip say why.
 */
function num(value: number, digits = 2): string {
  return Number.isFinite(value) ? value.toFixed(digits) : '—';
}

/**
 * A bar width as a percentage string, clamped to [0, 100].
 *
 * The clamp is not cosmetic. A non-finite input yields the CSS length `NaN%`,
 * which is invalid, so the browser discards the declaration and the element
 * falls back to its auto width -- frequently reading as a FULL bar. A step we
 * could not parse would then be drawn as the strongest one on screen. Zero is
 * the honest width for a quantity we do not have.
 */
function barPercent(value: number): string {
  if (!Number.isFinite(value)) return '0%';
  return `${Math.min(100, Math.max(0, value))}%`;
}

function bandColor(band: string): 'error' | 'warning' | 'info' | 'default' {
  switch (band) {
    case 'CERTAIN':
      return 'error';
    case 'HIGH':
      return 'warning';
    case 'MEDIUM':
      return 'info';
    default:
      return 'default';
  }
}

function EmptyNote({ children }: { children: React.ReactNode }) {
  return (
    <Box sx={{ p: 1 }}>
      <Typography variant="body2" sx={{ color: 'text.secondary', fontStyle: 'italic' }}>
        {children}
      </Typography>
    </Box>
  );
}

// ---------------------------------------------------------------------------
// weighted -- dedup
// ---------------------------------------------------------------------------

/** Each signal's share of total weight. Negative weights clamp to 0. */
function withShares(signals: WeightedSignal[]): Array<WeightedSignal & { share: number }> {
  const totalWeight = signals.reduce((sum, s) => sum + Math.max(0, s.weight), 0);
  return signals.map((s) => ({
    ...s,
    share: totalWeight > 0 ? Math.max(0, s.weight) / totalWeight : 0,
  }));
}

function WeightedView({ evidence }: { evidence: WeightedEvidence }) {
  if (evidence.signals.length === 0) {
    return (
      <EmptyNote>
        {evidence.emptyReason ?? 'No signal data available (pre-pipeline candidate).'}
      </EmptyNote>
    );
  }

  const rows = withShares(evidence.signals);

  return (
    <Box data-testid="evidence-weighted">
      <Stack direction="row" spacing={1} sx={{ alignItems: 'center', mb: 1.5 }}>
        <Typography variant="subtitle2" sx={{ fontWeight: 700 }}>
          Score: {num(evidence.score, 1)}
        </Typography>
        {evidence.band && (
          <Chip label={evidence.band} size="small" color={bandColor(evidence.band)} />
        )}
        {evidence.formula && (
          <Typography variant="caption" sx={{ color: 'text.disabled', ml: 'auto' }}>
            {evidence.formula}
          </Typography>
        )}
      </Stack>

      <Tooltip
        placement="bottom"
        title={
          <Box>
            {rows.map((s) => (
              <Typography key={s.id} variant="caption" sx={{ display: 'block' }}>
                {SIGNAL_LABELS[s.id] ?? s.label}: {num(s.share * 100, 1)}%
              </Typography>
            ))}
          </Box>
        }
      >
        <Box
          data-testid="evidence-stacked-bar"
          sx={{
            display: 'flex',
            height: 16,
            borderRadius: 1,
            overflow: 'hidden',
            mb: 1.5,
            bgcolor: 'action.disabledBackground',
          }}
        >
          {rows.map((s) => (
            <Box
              key={s.id}
              sx={(theme) => ({
                width: barPercent(s.share * 100),
                bgcolor: signalColor(theme, s.id),
                minWidth: s.share > 0 ? 2 : 0,
              })}
            />
          ))}
        </Box>
      </Tooltip>

      <Stack>
        {rows.map((s, i) => (
          <Tooltip key={s.id} title={s.detail || s.label} placement="left">
            <Stack
              direction="row"
              spacing={1}
              data-testid="evidence-signal-row"
              sx={zebraRowSx(i)}
            >
              <Box
                sx={(theme) => ({
                  width: 10,
                  height: 10,
                  borderRadius: '2px',
                  bgcolor: signalColor(theme, s.id),
                  flexShrink: 0,
                })}
              />
              <Typography variant="caption" sx={{ flex: 1, minWidth: 0 }} noWrap>
                {SIGNAL_LABELS[s.id] ?? s.label}
              </Typography>
              <Typography
                variant="caption"
                sx={{
                  color: 'text.secondary',
                  fontVariantNumeric: 'tabular-nums',
                  flexShrink: 0,
                }}
              >
                {num(s.value * 100, 0)}%
              </Typography>
              <Typography
                variant="caption"
                sx={{
                  color: 'text.disabled',
                  fontVariantNumeric: 'tabular-nums',
                  flexShrink: 0,
                  minWidth: 36,
                  textAlign: 'right',
                }}
              >
                w={num(s.weight)}
              </Typography>
            </Stack>
          </Tooltip>
        ))}
      </Stack>
    </Box>
  );
}

// ---------------------------------------------------------------------------
// facts -- regroup
// ---------------------------------------------------------------------------

function FactsView({ evidence }: { evidence: FactsEvidence }) {
  if (evidence.facts.length === 0) {
    return <EmptyNote>{evidence.emptyReason ?? 'No evidence recorded for this hold.'}</EmptyNote>;
  }

  return (
    <Box data-testid="evidence-facts">
      {evidence.headline && (
        <Typography variant="body2" sx={{ mb: 1 }}>
          {evidence.headline}
        </Typography>
      )}
      <Stack direction="row" spacing={0.5} useFlexGap sx={{ flexWrap: 'wrap' }}>
        {evidence.facts.map((f) => (
          <Tooltip key={f.label} title={f.hint} placement="top">
            <Chip
              size="small"
              label={f.label}
              color={f.warn ? 'warning' : 'default'}
              variant={f.warn ? 'filled' : 'outlined'}
            />
          </Tooltip>
        ))}
      </Stack>
    </Box>
  );
}

// ---------------------------------------------------------------------------
// waterfall -- metadata
// ---------------------------------------------------------------------------

/** How an operand reads in the reviewer's terms, given what the op does. */
function formatOperand(step: WaterfallStep): string {
  if (!Number.isFinite(step.operand)) return '—';
  switch (step.op) {
    case 'base':
      return num(step.operand);
    case 'multiply':
      return `×${num(step.operand)}`;
    case 'add':
      return `${step.operand >= 0 ? '+' : ''}${num(step.operand)}`;
    case 'replace':
      return `= ${num(step.operand)}`;
  }
}

/**
 * Whether a step helped or hurt. `replace` is deliberately neutral: it did not
 * raise or lower the evidence, it discarded it, and colouring it green because
 * the number happened to go up would imply the pipeline agreed.
 */
function stepTone(step: WaterfallStep, previousRunning: number): 'up' | 'down' | 'neutral' {
  if (step.op === 'base' || step.op === 'replace') return 'neutral';
  if (step.running > previousRunning) return 'up';
  if (step.running < previousRunning) return 'down';
  return 'neutral';
}

function toneColor(tone: 'up' | 'down' | 'neutral'): string {
  switch (tone) {
    case 'up':
      return 'success.main';
    case 'down':
      return 'error.main';
    default:
      return 'text.secondary';
  }
}

function WaterfallView({ evidence }: { evidence: WaterfallEvidence }) {
  if (evidence.steps.length === 0) {
    return (
      <EmptyNote>{evidence.emptyReason ?? 'No derivation recorded for this candidate.'}</EmptyNote>
    );
  }

  // Bars are scaled against the largest running total, not against 1.0: these
  // scores are unclamped and routinely exceed 1 once boost multipliers stack,
  // so a fixed 0-1 axis would peg most rows at full width and show nothing.
  const peak = Math.max(
    ...evidence.steps.map((s) => Math.abs(s.running)),
    Math.abs(evidence.score)
  );
  const scale = peak > 0 ? peak : 1;

  // The panel must not present a derivation that does not derive the score.
  // This is the same check the backend asserts as a property; repeating it here
  // guards against a stale or hand-built payload reaching the UI.
  //
  // Delegated to `waterfallIsConsistent` rather than re-comparing inline: the
  // predicate has to reject non-finite recompositions explicitly, and an inline
  // `Math.abs(a - b) > eps` gets that exactly backwards, since NaN compares
  // false against everything and so reads as agreement. Two copies of this test
  // is two chances to write the wrong polarity, so there is only one.
  const recomposed = recomposeWaterfall(evidence.steps);
  const inconsistent = !waterfallIsConsistent(evidence, 1e-6);

  return (
    <Box data-testid="evidence-waterfall">
      <Stack direction="row" spacing={1} sx={{ alignItems: 'center', mb: 1.5 }}>
        <Typography variant="subtitle2" sx={{ fontWeight: 700 }}>
          Score: {num(evidence.score)}
        </Typography>
        {inconsistent && (
          <Tooltip title={incompleteReason(recomposed, evidence.score)}>
            <Chip size="small" color="warning" label="breakdown incomplete" />
          </Tooltip>
        )}
      </Stack>

      <Stack>
        {evidence.steps.map((step, i) => {
          const previousRunning = i === 0 ? 0 : evidence.steps[i - 1].running;
          const tone = stepTone(step, previousRunning);
          return (
            <Tooltip key={step.id} title={step.detail ?? ''} placement="left">
              <Stack
                direction="row"
                spacing={1}
                data-testid="evidence-step-row"
                sx={zebraRowSx(i)}
              >
                <Typography variant="caption" sx={{ flex: 1, minWidth: 0 }} noWrap>
                  {step.label}
                  {step.capped && (
                    <Typography component="span" variant="caption" sx={{ color: 'text.disabled' }}>
                      {' '}
                      (capped)
                    </Typography>
                  )}
                </Typography>

                <Typography
                  variant="caption"
                  sx={{
                    color: toneColor(tone),
                    fontVariantNumeric: 'tabular-nums',
                    flexShrink: 0,
                    minWidth: 52,
                    textAlign: 'right',
                    fontWeight: step.op === 'replace' ? 700 : 400,
                  }}
                >
                  {formatOperand(step)}
                </Typography>

                <Box
                  sx={{
                    width: 72,
                    height: 8,
                    flexShrink: 0,
                    borderRadius: 1,
                    bgcolor: 'action.disabledBackground',
                    overflow: 'hidden',
                  }}
                >
                  <Box
                    sx={{
                      width: barPercent((Math.abs(step.running) / scale) * 100),
                      height: '100%',
                      bgcolor: step.op === 'replace' ? 'info.main' : 'primary.main',
                    }}
                  />
                </Box>

                <Typography
                  variant="caption"
                  sx={{
                    color: 'text.secondary',
                    fontVariantNumeric: 'tabular-nums',
                    flexShrink: 0,
                    minWidth: 44,
                    textAlign: 'right',
                  }}
                >
                  {num(step.running)}
                </Typography>
              </Stack>
            </Tooltip>
          );
        })}
      </Stack>
    </Box>
  );
}

// ---------------------------------------------------------------------------

export interface EvidencePanelProps {
  evidence: Evidence | null | undefined;
}

export function EvidencePanel({ evidence }: EvidencePanelProps) {
  if (!evidence) {
    return <EmptyNote>No evidence recorded.</EmptyNote>;
  }
  switch (evidence.kind) {
    case 'weighted':
      return <WeightedView evidence={evidence} />;
    case 'facts':
      return <FactsView evidence={evidence} />;
    case 'waterfall':
      return <WaterfallView evidence={evidence} />;
  }
}

export default EvidencePanel;
