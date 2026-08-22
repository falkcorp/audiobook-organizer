import json,subprocess,sys
REPO="/Users/jdfalk/repos/github.com/jdfalk/audiobook-organizer"
sel=json.load(open('g9-tasks.json'))
out={}
for t in sel:
    res=[]
    for a in t['verified_anchors']:
        cmd=a['grep_cmd']
        p=subprocess.run(['bash','-c',cmd],cwd=REPO,capture_output=True,text=True,timeout=120)
        o=p.stdout.strip()
        lines=o.splitlines()
        res.append({'claim':a['claim'][:120],'cmd':cmd,'expect':a.get('expect','')[:160],'rc':p.returncode,'n':len(lines) if o else 0,'out':lines[:6],'err':p.stderr.strip()[:200]})
    out[t['id']]=res
json.dump(out,open('anchors-9.json','w'),indent=1)
for tid,res in out.items():
    for r in res:
        flag='ZERO' if r['n']==0 else 'ok'
        print(f"{tid} [{flag} n={r['n']} rc={r['rc']}] {r['cmd']}")
        print(f"    expect: {r['expect']}")
        if r['n']==0 and r['err']: print("    err:",r['err'])
        for l in r['out'][:3]: print("    >",l[:160])
