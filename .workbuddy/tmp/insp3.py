import json
p=r'D:/Code/Git/loadout/.workbuddy/tmp/reqlog.json'
d=json.load(open(p,encoding='utf-8'))
req=d['request_json']
if isinstance(req,str): req=json.loads(req)
b=req['body']
if isinstance(b,str): b=json.loads(b)
msgs=b['messages']
for i,m in enumerate(msgs):
    r=m.get('role'); c=m.get('content')
    print(i, r, 'ctype', type(c).__name__)
    if r=='assistant':
        tc=m.get('tool_calls')
        print('   tool_calls:', json.dumps(tc,ensure_ascii=False)[:300] if tc else None)
    if r=='tool':
        print('   tool_call_id:', m.get('tool_call_id'))
print('--- tips tail ---')
rsp=d['response_json']
if isinstance(rsp,str): rsp=json.loads(rsp)
print(rsp.get('body','')[-350:] if isinstance(rsp.get('body'),str) else '')

