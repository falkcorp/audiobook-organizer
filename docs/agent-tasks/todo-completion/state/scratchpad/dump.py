import json,sys
sel=json.load(open('g9-tasks.json'))
ids=sys.argv[1:]
for t in sel:
    if t['id'] not in ids: continue
    print('='*70)
    print(t['id'],'|tier',t['tier'],'|eff',t['effort'],'|pol',t['polarity'],'|RC',t['review_critical'])
    print('TITLE:',t['title'])
    print('GOAL:',t['goal'])
    print('EXACT_FILES:',t['exact_files'])
    print('STEPS:')
    for i,s in enumerate(t['steps'],1): print('  %d. %s'%(i,s))
    print('TESTS:',t['tests'])
    print('AOS:',t['anti_over_suppression'])
    print('EDGE:',t['edge_cases'])
    print('ACCEPT:')
    a=t['acceptance']
    for x in (a if isinstance(a,list) else [a]): print('  -',x)
    print('GATE:',t.get('gate'))
    print('WHYTIER:',t['why_tier'])
