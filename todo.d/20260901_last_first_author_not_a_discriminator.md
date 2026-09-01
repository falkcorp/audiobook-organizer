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

Related: a mutation weakening `isMultiNameCredit`'s every-clause rule to
"any clause" survives the suite, and the only input separating the two versions
is exactly this shape — the weakened version gets `"Smith, John - Good Omens"`
*right*. The mutant is documented as an accepted survivor at
`internal/personname/personname.go` rather than killed, because killing it would
mean asserting the wrong answer in a test.
