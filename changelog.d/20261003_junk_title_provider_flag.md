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

By the owner's decision the fixer now repairs these. The recorded title is
weighed like a cached candidate (`provider_value`): for a title that still
carries its real name it must agree with that name or it is refused (the fetch
searched on the junk title and can hit another book: `01 The Shadow of Gods
01-34` has `Shadowfall` on record); where it disagrees with the folder the row
needs a person. A provider that returned the stored title itself never yields
a low-risk row. A user override still stops the repair, and the recorded value
is not rewritten.
