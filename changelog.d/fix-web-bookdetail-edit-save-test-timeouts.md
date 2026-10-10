### Fixed

#### BookDetail edit-save tests do far less query work per run

`BookDetail.edit-save.test.tsx` looked up dialog fields with `*ByLabelText` across
the whole page and polled those lookups. In jsdom each lookup walks the full
document, about 2,400 nodes, once for each of the roughly 90 labelable elements,
and any DOM mutation throws that result away. The tests now poll only for
`role=dialog` and then look up fields once, inside the dialog. On a dev laptop that
cuts the file's total test time from 7.5–13.9 s to 4.2–5.8 s, and its process CPU
from 12.2–14.5 s to 9.2–9.8 s. This lowers how exposed the 400-error test is to
Vitest's 30 s limit on the CPU-starved Woodpecker host, where it had timed out. The
30 s failure itself was not reproduced locally.

The 400 test also lets MUI's exit transition finish before it checks that the
dialog is still open. Before this change, a handler that closed the dialog still
passed the test, because a closing dialog stays in the DOM for 195 ms.
