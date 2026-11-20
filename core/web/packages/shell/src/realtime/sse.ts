// file: core/web/packages/shell/src/realtime/sse.ts
// version: 0.1.0
// guid: 64367fa9-f6aa-4b02-9c5d-531f5cad98c4
// last-edited: 2026-11-20

// The text/event-stream line parser of the fetch-based reader (07 §7.9.2,
// 05-realtime §4.2). The native stream has no `event:` line and no named
// events, so the only fields that matter are `id` and `data`; comment lines
// (`: hb`) and `retry:` are read and dropped (the client owns its own backoff).

/** One dispatched SSE message. `id` is set only when this frame carried an `id:` line. */
export interface SseMessage {
  id?: string;
  data: string;
}

export interface SseParser {
  /** Feeds decoded text; every complete message is passed to the callback in order. */
  push(chunk: string): void;
}

export function createSseParser(onMessage: (message: SseMessage) => void): SseParser {
  let buffer = '';
  let data: string[] = [];
  let id: string | undefined;

  function line(text: string): void {
    if (text === '') {
      if (data.length > 0) {
        const message: SseMessage = { data: data.join('\n') };
        if (id !== undefined) message.id = id;
        data = [];
        id = undefined;
        onMessage(message);
      } else {
        id = undefined;
      }
      return;
    }
    if (text.startsWith(':')) return; // comment, e.g. the `: hb` heartbeat
    const colon = text.indexOf(':');
    const field = colon === -1 ? text : text.slice(0, colon);
    let value = colon === -1 ? '' : text.slice(colon + 1);
    if (value.startsWith(' ')) value = value.slice(1);
    if (field === 'data') data.push(value);
    else if (field === 'id' && !value.includes('\0')) id = value;
    // `event` and `retry` are ignored on purpose (05-realtime §4.8 item 5).
  }

  return {
    push(chunk: string): void {
      buffer += chunk;
      let i = 0;
      let lineStart = 0;
      while (i < buffer.length) {
        const c = buffer.charCodeAt(i);
        if (c !== 10 && c !== 13) {
          i++;
          continue;
        }
        // A trailing CR may be the first half of CRLF: wait for the next chunk.
        if (c === 13 && i + 1 >= buffer.length) break;
        const text = buffer.slice(lineStart, i);
        i += c === 13 && buffer.charCodeAt(i + 1) === 10 ? 2 : 1;
        lineStart = i;
        line(text);
      }
      buffer = buffer.slice(lineStart);
    },
  };
}
