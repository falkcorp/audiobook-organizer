### "Last, First" is not used as a discriminator when choosing the author side

`personname.ChooseAuthorSide` picks which half of `"X - Y"` is the author using,
in order: a multi-name credit list, a leading article, then initials. It does not
use the strongest signal available for one common shape — **a person may be
written `"Last, First"`, and a title may not.**

Measured 2026-09-01, `origin/main` and `fix/person-name-unicode` alike:

```
"Gaiman, Neil - Anansi Boys"    -> author "Anansi Boys"    (want "Gaiman, Neil")
"Smith, John - Good Omens"      -> author "Good Omens"     (want "Smith, John")
"King, Stephen - The Stand"     -> author "King, Stephen"  correct, but only
                                   because the leading article rescues it
```

Pre-existing, not a regression — both trees answer identically except where the
article tiebreak happens to fire.

The fix is a fourth discriminator ahead of the tie: a side matching
`^\S+,\s+\S+$` whose halves are each name-shaped is a person in inverted form.
Two cautions before writing it:

- It must not fire on a genuine two-author comma credit
  (`"Neil Gaiman, Terry Pratchett"`), which is why it needs the *whole* string to
  be one inverted name rather than merely to contain a comma.
- `NormalizeAuthorName` in `internal/dedup` already un-inverts `"Last, First"`;
  check whether the discriminator belongs there instead, so the repo does not
  end up with two answers to "is this an inverted name?" — the same divergence
  that produced this package.

Related, and now superseded: this was originally filed alongside an accepted
mutation survivor in `isMultiNameCredit`. **That function has since been removed**
— it was a multi-CLAUSE test that filed omnibus titles as authors — so the
survivor and the reasoning attached to it are void. The underlying gap recorded
here is unaffected and still open: a last-first name (`"Smith, John"`) is not
used as a discriminator, and `"Smith, John - Good Omens"` is still answered
wrongly. Its replacement, `looksLikeAmpersandCredit`, was mutation-tested
separately: 8 mutants, 8 killed, no accepted survivors.
