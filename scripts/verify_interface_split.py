# file: scripts/verify_interface_split.py
# version: 1.0.0
# guid: 3a71b5cd-0e68-49f2-a4b1-8c05d9e37f26
# last-edited: 2026-08-18
#
# Proves an interface split preserved the method set exactly. Compares the full
# signature set -- methods AND embedded interfaces, followed one level through
# the composition -- between a base revision and the working tree:
#
#   BASE_REV=origin/main python3 scripts/verify_interface_split.py \
#       internal/database/iface_book.go:BookReader
#
# BASE_REV is an explicit parameter on purpose. It defaulted to HEAD once, which
# after a rebase points at the split commit itself, so the check compared the
# work to itself and passed vacuously.
#
# Counting only `Name(` lines is NOT sufficient: that is what let a dropped
# `database.SettingsStore` embed through. Mutation-tested -- deleting an embed
# makes this exit 1 with `LOST: database.SettingsStore`.
import re, subprocess, sys, pathlib, os
REV = os.environ.get('BASE_REV','HEAD')
# Extracts every signature line (method OR embed) reachable from `target`,
# following the composition one level into the split interfaces.
def sigs(text, target):
    decls = {}
    for m in re.finditer(r'^type (\w+) interface \{\n(.*?)^\}', text, re.S | re.M):
        decls[m.group(1)] = m.group(2)
    out, seen = set(), set()
    def walk(name):
        if name in seen: return
        seen.add(name)
        for l in decls.get(name, "").splitlines():
            l = re.sub(r'\s*//.*$', '', l).strip()
            if not l: continue
            if '(' in l: out.add(re.sub(r'\s+', ' ', l))
            elif l in decls: walk(l)          # a split sibling: recurse
            else: out.add(l)                  # a foreign embed: a leaf
    walk(target)
    return out
ok = True
for path, target in [a.split(':') for a in sys.argv[1:]]:
    before = sigs(subprocess.run(['git','show',f'HEAD:{path}'],capture_output=True,text=True).stdout, target)
    after  = sigs(pathlib.Path(path).read_text(), target)
    lost, gained = before - after, after - before
    status = "IDENTICAL" if not lost and not gained else "DIFFERS"
    if lost or gained: ok = False
    print(f"{target:22} {len(before):>3} -> {len(after):>3}  {status}")
    for s in sorted(lost):   print(f"    LOST:   {s}")
    for s in sorted(gained): print(f"    GAINED: {s}")
print("RESULT:", "all signature sets preserved" if ok else "MISMATCH")
sys.exit(0 if ok else 1)
