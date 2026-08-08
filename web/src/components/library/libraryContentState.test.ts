// file: web/src/components/library/libraryContentState.test.ts
// version: 1.0.0
// guid: 2b6f14ad-8c07-4d9e-9f31-5a70c8e2d4b6
// last-edited: 2026-08-08

import { describe, expect, it } from 'vitest';

import { libraryContentState } from './libraryContentState';

const ERR = new Error('Failed to fetch');

describe('libraryContentState', () => {
  it('shows the empty state only for a load that succeeded with zero books', () => {
    expect(
      libraryContentState({ bookCount: 0, loading: false, loadError: null, searchQuery: '' }),
    ).toBe('empty');
  });

  // The bug. A failed request also leaves the list empty with loading=false;
  // before the fix that rendered "No Audiobooks Found", telling the owner of a
  // 44,000-book library it was empty every time the backend restarted.
  it('never shows the empty state when the load failed', () => {
    expect(
      libraryContentState({ bookCount: 0, loading: false, loadError: ERR, searchQuery: '' }),
    ).toBe('reconnecting');
  });

  it('shows reconnecting during the post-deploy warmup window', () => {
    // Same shape as above: request settled, failed, nothing cached to show.
    const state = libraryContentState({
      bookCount: 0,
      loading: false,
      loadError: new Error('NetworkError when attempting to fetch resource.'),
      searchQuery: '',
    });
    expect(state).toBe('reconnecting');
    expect(state).not.toBe('empty');
  });

  it('defers to the normal body while a request is in flight', () => {
    expect(
      libraryContentState({ bookCount: 0, loading: true, loadError: null, searchQuery: '' }),
    ).toBe('content');
  });

  // A request can be in flight *and* the previous one have failed (a retry).
  // The in-flight state wins so the UI does not flip between two spinners.
  it('prefers the in-flight state over a stale error while retrying', () => {
    expect(
      libraryContentState({ bookCount: 0, loading: true, loadError: ERR, searchQuery: '' }),
    ).toBe('content');
  });

  it('never shows the empty state while books are on screen', () => {
    for (const loadError of [null, ERR]) {
      expect(
        libraryContentState({ bookCount: 20, loading: false, loadError, searchQuery: '' }),
      ).toBe('content');
    }
  });

  // Keeping the last known-good page on error is the other half of the fix:
  // a mid-session blip must leave the shelf intact rather than blanking it.
  it('keeps showing cached books when a refresh fails', () => {
    expect(
      libraryContentState({ bookCount: 20, loading: false, loadError: ERR, searchQuery: '' }),
    ).toBe('content');
  });

  it('lets the normal body handle a search that matched nothing', () => {
    expect(
      libraryContentState({ bookCount: 0, loading: false, loadError: null, searchQuery: 'zzz' }),
    ).toBe('content');
  });

  it('still reports reconnecting when a filtered search fails', () => {
    expect(
      libraryContentState({ bookCount: 0, loading: false, loadError: ERR, searchQuery: 'zzz' }),
    ).toBe('reconnecting');
  });

  it('returns exactly one state for every input combination', () => {
    const valid = new Set(['reconnecting', 'empty', 'content']);
    for (const bookCount of [0, 5]) {
      for (const loading of [true, false]) {
        for (const loadError of [null, ERR]) {
          for (const searchQuery of ['', 'query']) {
            const state = libraryContentState({ bookCount, loading, loadError, searchQuery });
            expect(valid.has(state)).toBe(true);
            // The invariant that matters: 'empty' requires a clean, settled,
            // genuinely-zero result. Nothing else may produce it.
            if (state === 'empty') {
              expect({ bookCount, loading, loadError, searchQuery }).toEqual({
                bookCount: 0,
                loading: false,
                loadError: null,
                searchQuery: '',
              });
            }
          }
        }
      }
    }
  });
});
