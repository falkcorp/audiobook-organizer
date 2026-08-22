import subprocess, re, sys, collections
todo = open('TODO.md').read()
todo_norm = re.sub(r'\s+',' ', todo.lower())
# list deleted fragment files with the commit that deleted them
out = subprocess.run(['git','log','--diff-filter=D','--name-only','--format=%H','--','todo.d/'],capture_output=True,text=True).stdout
commit=None; pairs=[]
for line in out.splitlines():
    if re.fullmatch(r'[0-9a-f]{40}', line): commit=line
    elif line.startswith('todo.d/') and line.endswith('.md') and 'README' not in line:
        pairs.append((commit,line))
missing=[]; present=0
seen=set()
for c,p in pairs:
    if p in seen: continue
    seen.add(p)
    body = subprocess.run(['git','show',f'{c}^:{p}'],capture_output=True,text=True).stdout
    # first heading or first task bullet
    heads=[l for l in body.splitlines() if l.startswith('#')]
    bullets=[l for l in body.splitlines() if re.match(r'\s*- \[ \]',l)]
    probes=[]
    for h in heads[:2]: probes.append(re.sub(r'^#+\s*','',h).strip())
    for b in bullets[:3]: probes.append(re.sub(r'^\s*- \[ \]\s*','',b).strip())
    probes=[re.sub(r'\s+',' ',x.lower())[:60] for x in probes if len(x)>15]
    hit=any(x in todo_norm for x in probes)
    if hit: present+=1
    else: missing.append((c[:8],p,probes[:2],len(body)))
print(f"deleted fragments: {len(seen)}  present in TODO.md: {present}  MISSING: {len(missing)}")
for m in missing: print(m[0],m[1],m[3],'|',m[2])
