// file: web/src/services/coreEvents/sseParser.ts
// version: 0.1.0
// guid: 06141206-d18a-4914-a6a9-4f3b315305c1
// last-edited: 2026-11-19

// Incremental parser for a text/event-stream byte stream (WHATWG HTML, "Server-sent
// events", section 9.2.6). It exists because the core stream is read with fetch and a
// ReadableStream (05-realtime 4.8 item 1), so nothing parses the wire for us.
//
// The server writes only unnamed frames (05-realtime 4.2): `id:` and `data:` lines, a
// `: hb` comment per heartbeat and a `retry:` line. The parser still implements the
// whole field grammar (event, multi-line data, CR / LF / CRLF endings, a leading BOM),
// so one parser serves a plain EventSource fallback too (05-realtime 4.8 item 5).

/** One dispatched frame. `id` is the last-event-ID buffer as the spec defines it. */
export interface SseFrame {
  /** Value of the last `id:` line seen on this stream so far ('' before any). */
  id: string;
  /** The `event:` name, 'message' when the frame names none. */
  event: string;
  /** Joined `data:` lines, no trailing newline. */
  data: string;
}

export interface SseParser {
  /** Feed one decoded chunk; returns the frames it completed, in order. */
  push(chunk: string): SseFrame[];
  /** Server `retry:` hint in ms, or null. The client owns its backoff and ignores it. */
  readonly retry: number | null;
}

/**
 * Creates a parser. Frames with no `data:` line are not dispatched (spec: an empty
 * data buffer resets the event without firing), so a lone `id:` or a comment-only
 * block yields nothing, but an `id:` still updates the id that later frames carry.
 */
export function createSseParser(): SseParser {
  let buffer = '';
  let first = true;
  let skipLF = false; // the previous chunk ended in CR: swallow a leading LF
  let id = '';
  let event = '';
  let data: string[] = [];
  let hasData = false;
  let retry: number | null = null;

  const processLine = (line: string, out: SseFrame[]) => {
    if (line === '') {
      if (hasData) out.push({ id, event: event || 'message', data: data.join('\n') });
      event = '';
      data = [];
      hasData = false;
      return;
    }
    if (line.startsWith(':')) return; // comment (the heartbeat)
    const colon = line.indexOf(':');
    const field = colon === -1 ? line : line.slice(0, colon);
    let value = colon === -1 ? '' : line.slice(colon + 1);
    if (value.startsWith(' ')) value = value.slice(1);
    switch (field) {
      case 'data':
        data.push(value);
        hasData = true;
        break;
      case 'event':
        event = value;
        break;
      case 'id':
        if (!value.includes('\0')) id = value;
        break;
      case 'retry':
        if (/^\d+$/.test(value)) retry = Number(value);
        break;
      default:
        break; // unknown fields are ignored
    }
  };

  return {
    push(chunk: string): SseFrame[] {
      const out: SseFrame[] = [];
      if (first && chunk.length > 0) {
        first = false;
        if (chunk.charCodeAt(0) === 0xfeff) chunk = chunk.slice(1);
      }
      if (skipLF && chunk.startsWith('\n')) chunk = chunk.slice(1);
      if (chunk.length > 0) skipLF = false;
      buffer += chunk;
      let start = 0;
      for (let i = 0; i < buffer.length; i++) {
        const c = buffer[i];
        if (c !== '\n' && c !== '\r') continue;
        if (c === '\r' && i === buffer.length - 1) {
          // A trailing CR may be the first half of CRLF: finish the line now and
          // remember to drop an LF that opens the next chunk.
          processLine(buffer.slice(start, i), out);
          start = i + 1;
          skipLF = true;
          break;
        }
        processLine(buffer.slice(start, i), out);
        if (c === '\r' && buffer[i + 1] === '\n') i++;
        start = i + 1;
      }
      buffer = buffer.slice(start);
      return out;
    },
    get retry() {
      return retry;
    },
  };
}
