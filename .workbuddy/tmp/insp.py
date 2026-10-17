import json
p=r'D:/Code/Git/loadout/.workbuddy/tmp/reqlog.json'
d=json.load(open(p,encoding='utf-8'))
print('model:', d['model'], '| http:', d['http_status'], '| stream:', d['stream'], '| result:', d['result'])
rj = d['request_json']
req = json.loads(rj) if isinstance(rj,str) else rj
print('req keys:', list(req.keys()) if isinstance(req,dict) else type(req))
msgs = req.get('messages') if isinstance(req,dict) else None
print('n_msgs:', len(msgs) if msgs else None)
if msgs:
    print('first msg role:', msgs[0].get('role'), 'len:', len(json.dumps(msgs[0],ensure_ascii=False)))
    print('last msg role:', msgs[-1].get('role'), 'len:', len(json.dumps(msgs[-1],ensure_ascii=False)))
print('tools:', len(req.get('tools') or []))
print('model field:', req.get('model'))
print('stream:', req.get('stream'))
resp = d['response_json']
rsp = json.loads(resp) if isinstance(resp,str) else resp
print('resp type:', type(rsp))

