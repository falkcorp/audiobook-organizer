### Changed

#### Junk-title repair — a provider's recorded title is evidence, not a stop sign

The junk-title fixer refused every book whose title had a provider value on
record (`skipped_provider_title`), reading that value as "a provider supplied
this title". Measured on prod on 2026-10-03 over the 1,233 rows the rule held
(555 of them number-leading): in 1,216 the recorded value is a *different*
title (`85 - Echoes of the System…` has `Echoes of the System: A LitRPG
Adventure` on record). A scheduled fetch records the provider's candidate and
then only fills empty fields, so the file-derived junk title stayed. In 11 a
provider did return the stored title.

By the owner's decision the fixer now repairs these. The recorded title is a
proposal source (`provider_value`) held to the cached candidate's standard:
for a title that still carries its real name it must agree with that name (the
fetch searched on the junk title and can hit another book: `01 The Shadow of
Gods 01-34` has `Shadowfall` on record); for a title with nothing in it
(`read by narrator`) it must have been recorded under the book's own author;
where it disagrees with the folder the row needs a person. A recorded title
that is refused, that cannot be read, or that is the stored title itself by
its letters and digits (`3-10 to Yuma` / `3:10 to Yuma`) never yields a
low-risk row, and the row says what was recorded and why it was not used. A
catalog title's bracketed edition marker (`(Unabridged)`) is dropped. A user
override still stops the repair, and the recorded value is not rewritten.
