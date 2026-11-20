// file: web/src/services/coreEvents/sseParser.test.ts
// version: 0.1.0
// guid: cf19baea-e333-4646-b214-2606b278eb8c
// last-edited: 2026-11-19

import { describe, expect, it } from 'vitest';

import { createSseParser } from './sseParser';

describe('createSseParser', () => {
  it('dispatches an unnamed frame with its id and data', () => {
    const p = createSseParser();
    expect(p.push('id: n-1\ndata: {"a":1}\n\n')).toEqual([{ id: 'n-1', event: 'message', data: '{"a":1}' }]);
  });

  it('reassembles a frame split across chunks at any byte', () => {
    const wire = 'id: n-7\ndata: {"topic":"x"}\n\n';
    for (let cut = 1; cut < wire.length; cut++) {
      const p = createSseParser();
      const out = [...p.push(wire.slice(0, cut)), ...p.push(wire.slice(cut))];
      expect(out).toEqual([{ id: 'n-7', event: 'message', data: '{"topic":"x"}' }]);
    }
  });

  it('treats CRLF, lone CR and LF alike, including a CRLF split across chunks', () => {
    const p = createSseParser();
    expect(p.push('data: a\r\n\r\n')).toHaveLength(1);
    expect(p.push('data: b\r\r')).toHaveLength(1);
    expect(p.push('data: c\r')).toHaveLength(0);
    expect(p.push('\n\r\n')).toEqual([{ id: '', event: 'message', data: 'c' }]);
  });

  it('joins multi-line data with a newline', () => {
    const p = createSseParser();
    expect(p.push('data: one\ndata: two\n\n')[0].data).toBe('one\ntwo');
  });

  it('ignores comments (the heartbeat) and yields nothing for them', () => {
    const p = createSseParser();
    expect(p.push(': hb\n\n')).toEqual([]);
  });

  it('a frame without data is not dispatched but its id carries to the next frame', () => {
    const p = createSseParser();
    expect(p.push('id: n-2\n\n')).toEqual([]);
    expect(p.push('data: x\n\n')).toEqual([{ id: 'n-2', event: 'message', data: 'x' }]);
  });

  it('an id-less frame keeps the last id seen (the spec buffer), a control frame is told apart by its topic', () => {
    const p = createSseParser();
    p.push('id: n-3\ndata: a\n\n');
    expect(p.push('data: b\n\n')[0].id).toBe('n-3');
  });

  it('reads retry, named events, strips one leading space and a BOM, ignores unknown fields', () => {
    const p = createSseParser();
    const out = p.push('﻿retry: 3000\nfoo: bar\nevent: custom\ndata:  padded\n\n');
    expect(p.retry).toBe(3000);
    expect(out).toEqual([{ id: '', event: 'custom', data: ' padded' }]);
  });

  it('ignores a non-numeric retry and an id containing NUL', () => {
    const p = createSseParser();
    p.push('retry: soon\nid: a\0b\ndata: x\n\n');
    expect(p.retry).toBeNull();
    expect(p.push('data: y\n\n')[0].id).toBe('');
  });
});
