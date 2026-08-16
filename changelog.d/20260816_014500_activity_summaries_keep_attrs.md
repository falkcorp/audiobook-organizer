### Fixed

#### Activity-log rows no longer drop the data they are about

The activity log showed entries that had lost the only information that made
them worth recording — "cover art saved to" (to *where*?), "ISBN enrichment
succeeded for" (for *what*?). These are `slog` calls whose sentence is the
**message** and whose content is in the **attributes**, and the log-line bridge
kept only the message.

A neighbouring row showed the opposite symptom: a raw slog line with its quotes
pasted into the summary, e.g.

```
ISBN enrichment found" isbn="9780553293357" title="Foundation
```

Both come from **one** defect. The message was located with
`strings.LastIndexByte(rest, '"')`, which finds the last quote in the whole
remaining line rather than the message's own closing quote. When nothing after
`msg=` was quoted it landed on the right character by luck; when any attribute
was quoted, the "message" swallowed the rest of the line, stray quote included.
That is why it looked like two inconsistent bridges rather than one bug.

The line is now scanned once into structured `key=value` attributes, honouring
quoted values that contain spaces, `=` or escaped quotes. Attributes are
rendered into the summary and also stored in `details` so they stay queryable
rather than existing only as a substring:

- A message ending in a preposition or a colon is a sentence fragment, so its
  first attribute is appended bare — *"cover art saved to /lib/Asimov/cover.jpg"*.
- Any other message gets `key=value` appended — *"tag writing failed book_id=b7
  error=disk full"*.
- `op_id`, `component` and the other structural keys keep being lifted into
  their own fields and are not repeated in the summary.
