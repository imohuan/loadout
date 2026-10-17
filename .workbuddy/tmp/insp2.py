import json
p=r'D:/Code/Git/loadout/.workbuddy/tmp/reqlog.json'
d=json.load(open(p,encoding='utf-8'))
req=json.loads(d['request_json']) if isinstance(d['request_json'],str) else d['request_json']
body=req.get('body')
b=json.loads(body) if isinstance(body,str) else body
print('body keys:', list(b.keys())[:20])
msgs=b.get('messages') or []
print('n_msgs:', len(msgs))
for i,m in enumerate(msgs):
    c=m.get('content')
    l=len(json.dumps(c,ensure_ascii=False)) if c is not None else 0
    print(i, m.get('role'), 'len', l, 'type', type(c).__name__)
    if isinstance(c,str) and len(c)<200: print('   ', repr(c[:150]))
rsp=json.loads(d['response_json']) if isinstance(d['response_json'],str) else d['response_json']
print('--- response ---')
print(json.dumps(rsp, ensure_ascii=False)[:600])

