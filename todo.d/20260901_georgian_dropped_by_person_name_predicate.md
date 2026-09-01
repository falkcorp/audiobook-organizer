## `internal/personname` silently drops every Georgian author (and the obvious fix is inert)

`personname.LooksLikePersonName("გიორგი ბაქრაძე")` returns **false**, so Georgian
authors are dropped at all five call sites (scanner ×3, metadata ×2, dedup's
splitter). Found 2026-09-01 during review of #3029.

**Cause.** The package's central rule is "the first rune must be a letter and must
NOT be lowercase", chosen over "must be uppercase" because `unicode.IsUpper` is
false for every caseless script (CJK, Hebrew, Arabic, Thai). That formulation is
correct for *caseless* scripts and wrong for a **cased script whose default written
form is the lowercase one**. Georgian Mkhedruli letters are Unicode `Ll` —
`unicode.IsLower('გ') == true` — because Unicode 11 added Mtavruli capitals, yet
Mkhedruli is how Georgian is normally written. So every Georgian name looks like a
title fragment.

Not a regression: main's ASCII byte test dropped Georgian too. But it is precisely
the failure `internal/personname` was extracted to eliminate, and it was not on the
package's known-limits list.

**⚠️ The obvious fix does NOT work — measured, do not re-propose it.** The natural
remedy is "accept a first rune that has no uppercase mapping", i.e. treat
`unicode.ToUpper(r) == r` as acceptable. Go maps Mkhedruli to Mtavruli:

```
'გ'  IsLower=true  IsUpper=false  ToUpper='Გ'  ToUpper==r: false
'ბ'  IsLower=true  IsUpper=false  ToUpper='Ბ'  ToUpper==r: false
'春'  IsLower=false IsUpper=false  ToUpper='春'  ToUpper==r: true
```

So that test rejects Georgian exactly as today. Armenian lowercase (`'ա'` →
`ToUpper='Ա'`) behaves the same way, so Armenian names written in lowercase are in
the same class.

**What would actually work** needs a decision, which is why this is filed rather
than fixed: the check has to know that a script's *default* form is lowercase.
That means a per-script exception (`unicode.Georgian`, and probably
`unicode.Armenian`, `unicode.Deseret`, `unicode.Adlam`, `unicode.Cherokee`,
`unicode.Warang_Citi`, `unicode.Osage`, `unicode.Vithkuqi`) rather than a general
Unicode property, because no property distinguishes "cased script normally written
lowercase" from "lowercase word in a bicameral script".

- [ ] Decide the exception mechanism, then fix `LooksLikePersonName` and add
      Georgian and Armenian cases to `internal/personname/personname_test.go`
      and to the differential corpus.
- [ ] Until then, record Georgian and lowercase-Armenian in the package doc's
      known-limits list, which currently implies non-Latin scripts are handled.
