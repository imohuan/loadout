import json
p=r'D:/Code/Git/loadout/.workbuddy/tmp/reqlog.json'
d=json.load(open(p,encoding='utf-8'))
print('started_at:', d['started_at'])
print('path:', d['request_json'].get('path') if isinstance(d['request_json'],dict) else '?')
print('channel:', d.get('channel'))

