### Changed

#### Junk-title repair — a provider-recorded title that fails the junk classifier is repaired

The junk-title fixer refused every book whose title had a provider value on
record (`skipped_provider_title`), on the rule that a provider's answer is
never overwritten from a folder or a prefix strip. On 2026-10-03 that rule
held 350 books titled like `02 - No Quarter` and `01 - Prador Moon`: no
provider returns such a title, the value is a file-derived title a refetch
echoed back. By the owner's decision the fixer now repairs a provider-recorded
title when it fails the junk classifier, through the same proposal gates as
any other junk title, and the row's reason says the stored title was recorded
as provider-supplied. A user override still stops the repair. The provider
flag itself is not rewritten.
