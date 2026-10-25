// file: web/src/utils/permissionError.test.ts
// version: 1.0.0
// guid: f0377c63-3ef6-4dd2-a025-978a6d342cb9
// last-edited: 2026-10-25

import { describe, it, expect } from 'vitest';
import { ApiError } from '../services/api';
import { describePermissionDenied } from './permissionError';

describe('describePermissionDenied', () => {
  it('names the action and keeps the server wording for a 403', () => {
    const err = new ApiError('permission denied: library.delete', 403);
    expect(describePermissionDenied(err, 'move books to the trash')).toBe(
      "You don't have permission to move books to the trash (permission denied: library.delete)."
    );
  });

  it('still reads cleanly when the 403 has no message', () => {
    expect(describePermissionDenied(new ApiError('  ', 403), 'restore books')).toBe(
      "You don't have permission to restore books."
    );
  });

  it('is null for any other failure so the caller keeps its own message', () => {
    expect(describePermissionDenied(new ApiError('boom', 500), 'x')).toBeNull();
    expect(describePermissionDenied(new ApiError('nope', 401), 'x')).toBeNull();
    expect(describePermissionDenied(new Error('network'), 'x')).toBeNull();
    expect(describePermissionDenied(undefined, 'x')).toBeNull();
  });
});
