// file: web/src/components/review/QueueRail.unreviewable.test.ts
// version: 1.0.0
// guid: 4a1c8e35-9b72-4d06-8f13-2e5a7c94b0d8
// last-edited: 2026-08-20

/**
 * The "N unreviewable" chip's tooltip.
 *
 * The chip used to show a bare count with a tooltip that named the possible
 * causes without saying which applied. On production it read "8532
 * unreviewable" over text that could equally have described 8,532 orphans or
 * 8,532 refetchable rows -- two situations with nothing in common except the
 * number. These tests pin that the tooltip now reports the actual split, and
 * that it degrades honestly when the server does not send one.
 */

import { describe, it, expect } from 'vitest';
import { unreviewableReason } from './QueueRail';

describe('the unreviewable tooltip', () => {
  it('names each cause with its own count', () => {
    const text = unreviewableReason({
      orphaned: 3354,
      no_candidates: 5178,
      decode_errors: 2,
    });

    expect(text).toContain('3,354');
    expect(text).toContain('5,178');
    expect(text).toContain('2');
    expect(text).toMatch(/no longer exists/);
    expect(text).toMatch(/no candidate stored/);
    expect(text).toMatch(/will not decode/);
  });

  it('states the remedy, because the causes need opposite ones', () => {
    const text = unreviewableReason({ orphaned: 10, no_candidates: 20, decode_errors: 0 });

    // The whole point of the split: one of these is reapable and the other is
    // refetchable, and the reader has to be able to tell which is which.
    expect(text).toMatch(/cleanup pass/);
    expect(text).toMatch(/refetch/);
  });

  it('omits causes that contributed nothing', () => {
    const text = unreviewableReason({ orphaned: 7, no_candidates: 0, decode_errors: 0 });

    expect(text).toContain('7');
    expect(text).not.toMatch(/no candidate stored/);
    expect(text).not.toMatch(/will not decode/);
  });

  it('falls back to naming the causes when the server sends no breakdown', () => {
    // An older server omits the split entirely. Claiming every cause is zero
    // would be a statement the payload never made.
    const text = unreviewableReason(undefined);

    expect(text).toMatch(/no candidate stored/);
    expect(text).toMatch(/no longer exists/);
    expect(text).not.toMatch(/\d/);
  });
});
