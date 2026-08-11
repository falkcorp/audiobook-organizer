// file: web/src/components/dedup/FingerprintCanvas.tsx
// version: 1.1.0
// guid: c7d8e9f0-a1b2-4c3d-8e4f-5a6b7c8d9e0f
// last-edited: 2026-08-10
// FingerprintCanvas renders a chromaprint base64 fingerprint as a visual
// bit-matrix heatmap. Each row = one time bucket (aggregated frames), each
// column = one of the 32 bit positions. Cells are colored by bit value and
// column position so frequency structure is visible at a glance.
//
// Two completely different books produce visually distinct patterns;
// the same content in different formats produces nearly identical ones.
// This makes false-positive dedup candidates obvious by eye.

import { type CSSProperties, type RefObject, useEffect, useRef } from 'react';
import { Box, Tooltip, Typography } from '@mui/material';

interface FingerprintCanvasProps {
  /** Base64-encoded chromaprint fingerprint string (AcoustIDSeg0 format). */
  fingerprint: string;
  /** Label shown above the canvas (e.g. "Book A"). */
  label?: string;
  /** Canvas width in px. Height is derived from aspect ratio. Default 180. */
  width?: number;
  /** Number of time-bucket rows to render. Default 48. */
  rows?: number;
}

// Decode a chromaprint base64 string into an array of uint32 frames.
// Chromaprint base64: 4-byte version header + N×4-byte LE uint32 frames.
function decodeChromaprint(b64: string): Uint32Array | null {
  try {
    const raw = atob(b64);
    const bytes = new Uint8Array(raw.length);
    for (let i = 0; i < raw.length; i++) bytes[i] = raw.charCodeAt(i);
    // Skip 4-byte header
    if (bytes.length < 8) return null;
    const frameBytes = bytes.slice(4);
    const frames = new Uint32Array(
      frameBytes.buffer,
      frameBytes.byteOffset,
      Math.floor(frameBytes.byteLength / 4),
    );
    return frames.length > 0 ? frames : null;
  } catch {
    return null;
  }
}

// Aggregate frames into `rows` buckets by majority-bit per column.
// Returns a Uint8Array of length rows×32 where each byte is 0 or 1.
function bucketBits(frames: Uint32Array, rows: number): Uint8Array {
  const result = new Uint8Array(rows * 32);
  const bucketSize = Math.max(1, Math.floor(frames.length / rows));
  for (let row = 0; row < rows; row++) {
    const start = row * bucketSize;
    const end = Math.min(start + bucketSize, frames.length);
    for (let bit = 0; bit < 32; bit++) {
      let ones = 0;
      let count = 0;
      for (let f = start; f < end; f++) {
        ones += (frames[f] >>> bit) & 1;
        count++;
      }
      result[row * 32 + bit] = count > 0 && ones > count / 2 ? 1 : 0;
    }
  }
  return result;
}

// Map bit column (0-31) to a hue. Low bits (bass/low-freq) → warm colors;
// high bits (treble/high-freq) → cool colors. Produces a rainbow-ish palette
// that makes column structure visible at a glance.
function colForBit(bit: number): string {
  const hue = Math.round((bit / 31) * 260); // 0=red … 260=blue-violet
  return `hsl(${hue}, 90%, 55%)`;
}

// Off color — dark but not pure black so the canvas background is obvious.
const OFF_COLOR = '#0d0d1a';
const EMPTY_COLOR = '#1e1e2e';

export function FingerprintCanvas({
  fingerprint,
  label,
  width = 180,
  rows = 48,
}: FingerprintCanvasProps) {
  const canvasRef = useRef<HTMLCanvasElement>(null);
  const COLS = 32;
  // Each cell: ceil(width/32) px wide, 3 px tall (gives a ~180×144 default canvas)
  const cellW = Math.max(2, Math.floor(width / COLS));
  const cellH = 3;
  const canvasW = cellW * COLS;
  const canvasH = cellH * rows;

  useEffect(() => {
    const canvas = canvasRef.current;
    if (!canvas) return;
    const ctx = canvas.getContext('2d');
    if (!ctx) return;

    ctx.fillStyle = EMPTY_COLOR;
    ctx.fillRect(0, 0, canvasW, canvasH);

    if (!fingerprint) return;

    const frames = decodeChromaprint(fingerprint);
    if (!frames) return;

    const bits = bucketBits(frames, rows);

    // Pre-compute column colors once
    const colors = Array.from({ length: COLS }, (_, bit) => colForBit(bit));

    for (let row = 0; row < rows; row++) {
      for (let col = 0; col < COLS; col++) {
        const on = bits[row * COLS + col];
        ctx.fillStyle = on ? colors[col] : OFF_COLOR;
        ctx.fillRect(col * cellW, row * cellH, cellW - 1, cellH - 1);
      }
    }
  }, [fingerprint, rows, canvasW, canvasH, cellW, cellH]);

  const isEmpty = !fingerprint;
  const tooltipText = isEmpty
    ? 'No fingerprint data'
    : `Chromaprint bit-matrix — ${rows} time buckets × 32 frequency bits`;

  return (
    <Tooltip title={tooltipText} placement="top">
      <Box sx={{ display: 'inline-flex', flexDirection: 'column', gap: 0.5, alignItems: 'flex-start' }}>
        {label && (
          <Typography
            variant="caption"
            color="text.secondary"
            sx={{ fontWeight: 600, textTransform: 'uppercase', letterSpacing: 0.5, fontSize: '0.6rem' }}
          >
            {label}
          </Typography>
        )}
        <Box
          sx={{
            borderRadius: 1,
            overflow: 'hidden',
            border: '1px solid',
            borderColor: 'divider',
            lineHeight: 0,
          }}
        >
          <canvas
            ref={canvasRef}
            width={canvasW}
            height={canvasH}
            style={{ display: 'block', imageRendering: 'pixelated' }}
            aria-label={label ? `Fingerprint for ${label}` : 'Fingerprint visualization'}
          />
        </Box>
        {isEmpty && (
          <Typography variant="caption" color="text.disabled" sx={{ fontSize: '0.6rem' }}>
            No data
          </Typography>
        )}
      </Box>
    </Tooltip>
  );
}

// FingerprintPair shows two fingerprints side-by-side with a subtle diff
// highlight. Cells that differ between A and B get a faint amber overlay,
// making mis-matches pop visually.
interface FingerprintPairProps {
  hashA: string;
  hashB: string;
  width?: number;
  rows?: number;
}

export function FingerprintPair({ hashA, hashB, width = 180, rows = 48 }: FingerprintPairProps) {
  const canvasARef = useRef<HTMLCanvasElement>(null);
  const canvasBRef = useRef<HTMLCanvasElement>(null);
  const diffRef = useRef<HTMLCanvasElement>(null);

  const COLS = 32;
  const cellW = Math.max(2, Math.floor(width / COLS));
  const cellH = 3;
  const canvasW = cellW * COLS;
  const canvasH = cellH * rows;

  useEffect(() => {
    const framesA = hashA ? decodeChromaprint(hashA) : null;
    const framesB = hashB ? decodeChromaprint(hashB) : null;
    const bitsA = framesA ? bucketBits(framesA, rows) : new Uint8Array(rows * COLS);
    const bitsB = framesB ? bucketBits(framesB, rows) : new Uint8Array(rows * COLS);
    const colors = Array.from({ length: COLS }, (_, bit) => colForBit(bit));

    function renderCanvas(
      ref: RefObject<HTMLCanvasElement | null>,
      bits: Uint8Array,
      hasData: boolean,
    ) {
      const canvas = ref.current;
      if (!canvas) return;
      const ctx = canvas.getContext('2d');
      if (!ctx) return;
      ctx.fillStyle = EMPTY_COLOR;
      ctx.fillRect(0, 0, canvasW, canvasH);
      if (!hasData) return;
      for (let row = 0; row < rows; row++) {
        for (let col = 0; col < COLS; col++) {
          const on = bits[row * COLS + col];
          ctx.fillStyle = on ? colors[col] : OFF_COLOR;
          ctx.fillRect(col * cellW, row * cellH, cellW - 1, cellH - 1);
        }
      }
    }

    renderCanvas(canvasARef, bitsA, framesA != null);
    renderCanvas(canvasBRef, bitsB, framesB != null);

    // Diff canvas: highlight cells that differ with amber
    const diffCanvas = diffRef.current;
    if (diffCanvas && framesA && framesB) {
      const ctx = diffCanvas.getContext('2d');
      if (ctx) {
        ctx.clearRect(0, 0, canvasW, canvasH);
        for (let row = 0; row < rows; row++) {
          for (let col = 0; col < COLS; col++) {
            const idx = row * COLS + col;
            if (bitsA[idx] !== bitsB[idx]) {
              ctx.fillStyle = 'rgba(255, 160, 0, 0.45)';
              ctx.fillRect(col * cellW, row * cellH, cellW - 1, cellH - 1);
            }
          }
        }
      }
    }
  }, [hashA, hashB, rows, canvasW, canvasH, cellW, cellH]);

  // Count bit-level similarity for the similarity badge
  const simPct = (() => {
    const framesA = hashA ? decodeChromaprint(hashA) : null;
    const framesB = hashB ? decodeChromaprint(hashB) : null;
    if (!framesA || !framesB) return null;
    const bitsA = bucketBits(framesA, rows);
    const bitsB = bucketBits(framesB, rows);
    let match = 0;
    const total = rows * COLS;
    for (let i = 0; i < total; i++) if (bitsA[i] === bitsB[i]) match++;
    return Math.round((match / total) * 100);
  })();

  const canvasStyle: CSSProperties = {
    display: 'block',
    imageRendering: 'pixelated',
    position: 'absolute',
    top: 0,
    left: 0,
  };

  return (
    <Box sx={{ display: 'flex', flexDirection: 'column', gap: 1 }}>
      {simPct != null && (
        <Typography variant="caption" color="text.secondary" sx={{ textAlign: 'center', display: 'block' }}>
          Visual similarity:{' '}
          <Box
            component="span"
            sx={[{
              fontWeight: 700
            }, simPct > 80 ? {
              color: 'success.main'
            } : {
              color: simPct > 50 ? 'warning.main' : 'error.main'
            }]}
          >
            {simPct}%
          </Box>
          {' '}(amber = differs)
        </Typography>
      )}
      <Box sx={{ display: 'flex', gap: 2, justifyContent: 'center', flexWrap: 'wrap' }}>
        <Box sx={{ display: 'flex', flexDirection: 'column', gap: 0.5, alignItems: 'flex-start' }}>
          <Typography
            variant="caption"
            color="text.secondary"
            sx={{ fontWeight: 600, textTransform: 'uppercase', letterSpacing: 0.5, fontSize: '0.6rem' }}
          >
            Book A
          </Typography>
          <Box
            sx={{
              borderRadius: 1,
              overflow: 'hidden',
              border: '1px solid',
              borderColor: 'divider',
              lineHeight: 0,
              position: 'relative',
              width: canvasW,
              height: canvasH,
            }}
          >
            <canvas ref={canvasARef} width={canvasW} height={canvasH} style={canvasStyle} />
            <canvas
              ref={diffRef}
              width={canvasW}
              height={canvasH}
              style={{ ...canvasStyle, pointerEvents: 'none' } as CSSProperties}
              aria-hidden
            />
          </Box>
        </Box>
        <Box sx={{ display: 'flex', flexDirection: 'column', gap: 0.5, alignItems: 'flex-start' }}>
          <Typography
            variant="caption"
            color="text.secondary"
            sx={{ fontWeight: 600, textTransform: 'uppercase', letterSpacing: 0.5, fontSize: '0.6rem' }}
          >
            Book B
          </Typography>
          <Box
            sx={{
              borderRadius: 1,
              overflow: 'hidden',
              border: '1px solid',
              borderColor: 'divider',
              lineHeight: 0,
              position: 'relative',
              width: canvasW,
              height: canvasH,
            }}
          >
            <canvas ref={canvasBRef} width={canvasW} height={canvasH} style={canvasStyle} />
          </Box>
        </Box>
      </Box>
    </Box>
  );
}
