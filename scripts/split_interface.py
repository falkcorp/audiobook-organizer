# file: scripts/split_interface.py
# version: 1.0.0
# guid: 9c4d2e81-73af-4b06-8d59-1e2f6a80b7c3
# last-edited: 2026-08-18
#
# Splits one wide Go interface into several focused ones, retaining the original
# name as their composition so the method set is byte-identical and no consumer
# moves. Import `split` and call it with the groups you want:
#
#   split("internal/database/iface_book.go", "BookReader", [
#       ("BookLookup", "resolves books by identity.", ["GetBook", "GetBookByPath"]),
#       ...
#   ])
#
# Two bugs this has already been fixed for, both of which produced silent or
# confusing damage -- do not "simplify" them away:
#
#   * Multi-line signatures. Three of ServerDeps' 43 methods wrap across lines.
#     Reading one line per method left a stray continuation behind and produced
#     `syntax error: unexpected }, expected )`. Hence the paren-depth walk.
#   * Embedded interfaces with a trailing comment. SystemStore embeds
#     `database.SettingsStore // factoryReset -> config.SaveConfigToDatabase`.
#     An end-of-line-anchored regex missed it, the embed was dropped, and the
#     only reason it was caught is that a caller happened to need the embedded
#     type. Always verify with scripts/verify_interface_split.py, which compares
#     embeds as well as methods.
import re, pathlib
def split(path, target, groups):
    p = pathlib.Path(path); lines = p.read_text().splitlines()
    start = next(i for i,l in enumerate(lines) if l.startswith(f"type {target} interface {{"))
    end   = next(i for i in range(start+1,len(lines)) if lines[i] == "}")
    hc = start
    while hc>0 and lines[hc-1].startswith("//"): hc -= 1
    doc, body = lines[hc:start], lines[start+1:end]
    units, embeds, pending, i = {}, [], [], 0
    while i < len(body):
        l = body[i]
        m = re.match(r'^\t([A-Za-z0-9_]+)\(', l)
        if m:
            chunk = pending+[l]; depth = l.count("(")-l.count(")"); i += 1
            while depth > 0 and i < len(body):
                chunk.append(body[i]); depth += body[i].count("(")-body[i].count(")"); i += 1
            units[m.group(1)] = "\n".join(chunk); pending = []; continue
        e = re.match(r'^\t([A-Za-z_][A-Za-z0-9_.]*)\s*(//.*)?$', l)   # embedded interface (may carry a trailing comment)
        if e:
            embeds.append("\n".join(pending+[l])); pending = []; i += 1; continue
        if l.strip().startswith("//"): pending.append(l)
        elif l.strip()=="": pending=[]
        else: pending.append(l)
        i += 1
    named=[m for _,_,ms in groups for m in ms]
    assert sorted(named)==sorted(units), f"{target}: unplaced={sorted(set(units)-set(named))} phantom={sorted(set(named)-set(units))}"
    assert len(named)==len(set(named)), f"{target}: duplicate"
    out=[]
    for n,d,ms in groups:
        out += [f"// {n} {d}", f"type {n} interface {{"] + [units[m] for m in ms] + ["}",""]
    out += doc + ["//",
      f"// Split into the {len(groups)} interfaces above on 2026-08-18. This name is retained as",
      "// their composition so the method set is byte-identical and no consumer moves; the",
      "// type checker proves it.",
      f"type {target} interface {{"] + [f"\t{n}" for n,_,_ in groups]
    if embeds:                      # embedded interfaces are carried through verbatim
        out += ["", "\t// Embedded interfaces carried through from the original declaration."] + embeds
    out += ["}"]
    p.write_text("\n".join(lines[:hc]+out+lines[end+1:]).rstrip()+"\n")
    print(f"  {target}: {len(units)} methods + {len(embeds)} embed(s) -> {len(groups)} interfaces {[len(ms) for _,_,ms in groups]}")
