import json,re,os,glob,sys
ROOT='/private/tmp/claude-501/-Users-jdfalk-repos-github-com-jdfalk-audiobook-organizer/f21a92f9-ff10-4ce5-a715-d13a59db3783/scratchpad/dryrun/docs/agent-tasks/todo-completion'
REPO='/Users/jdfalk/repos/github.com/jdfalk/audiobook-organizer'
sk=json.load(open(ROOT+'/skeleton.json'))
by={t['id']:t for t in sk['tasks']}
ids=['TASK-%03d'%n for n in [90,91,92,93,94,95,96,97,98,99,100,101,102,103,104,105,106,107,108,109,110,111,112,113,114,198,199,200,201,202,194,196,176,190,212,213,115,116,117,118]]
pat=re.compile(r'(?:[\w.@-]+/)+[\w.@-]+\.(?:go|tsx|ts|json|yml|yaml|md|sh|py|service|sql|mjs)')
for i in ids:
    t=by[i]
    f=glob.glob(f"{ROOT}/{t['ws']}/{i}-*.md")[0]
    txt=open(f).read()
    # strip header comment + START HERE block
    body=txt
    toks=sorted(set(pat.findall(body)))
    ef=set(t['exact_files'])
    extra=[x for x in toks if x not in ef and not x.startswith('docs/agent-tasks') and not x.startswith('changelog.d') and not x.startswith('todo.d')]
    missing_on_disk=[x for x in toks if not os.path.exists(os.path.join(REPO,x))]
    ef_missing=[x for x in ef if not os.path.exists(os.path.join(REPO,x))]
    print('====',i,t['ws'])
    print('  EF:',sorted(ef))
    print('  prose-not-in-EF:',extra)
    print('  EF-not-on-disk:',ef_missing)
    print('  prose-path-not-on-disk:',missing_on_disk)
