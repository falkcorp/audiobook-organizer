### Fixed

#### A library scan no longer stops dead on a single unreadable file

Scanning your library has been giving up partway through for the last three
days, always at the same place. It reported the same 14,912 books processed
every time and then stopped, so anything past that point in the library was
never looked at — and because the count it showed kept changing slightly
(40,109 books one run, 40,089 the next), it looked like the scan was making
progress when it was not.

One file was stopping everything. Reading a file's tags means handing it to a
parser that trusts what the file says about its own structure, and a file that
lies about that can send the parser into a read that never finishes. There was
no time limit on that step and no way to interrupt it, so cancelling the scan
did not help either: the scan was told to stop, said it had stopped, and the
underlying read carried on forever.

Reading a single file is now given a generous time limit — long enough that a
big file on a slow network drive is never affected — and a file that exceeds it
is treated the same way as any other unreadable file. The scan notes the
failure, falls back to naming the book from its filename, and moves on to the
next one instead of stopping.

The scan will now finish. The file that caused this is still unreadable, and it
will now show up as a failure you can see rather than as a scan that quietly
never ended.
