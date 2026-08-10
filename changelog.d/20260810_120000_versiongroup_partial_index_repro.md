<!-- TODO.md evidence only — no code, no behaviour change, so this fragment is
     deliberately a no-op comment. See changelog.d/README.md.

     Records a deterministic reproduction of the GetBooksByVersionGroup
     under-report (open item, found in prod 2026-08-06). Dropping exactly ONE
     book:versiongroup:<gid>:<id> row hides that member; dropping BOTH returns
     both, because the len(books) > 0 guard only falls back on an EMPTY index.
     Losing more index data produces a more correct answer.

     No fix is included: the read path feeds metafetch's sibling-writeback
     enumeration, and the entry asks for the fix direction to be chosen after
     measuring how prevalent partial indexes are in prod — which needs prod
     access. A fourth direction is added (memdb already carries a complete
     VersionGroupID index that this function never consults). The test is left
     uncommitted rather than committed red or skipped. -->
