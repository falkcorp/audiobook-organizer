import json,sys
base='patches/verify-7.json'
a=json.load(open(base)); b=json.load(open('add.json'))
seen={x['task_id'] for x in a}
for x in b:
    if x['task_id'] in seen:
        a=[y for y in a if y['task_id']!=x['task_id']]
    a.append(x)
json.dump(a,open(base,'w'),indent=1,ensure_ascii=False)
print(len(a),[x['task_id'] for x in a])
