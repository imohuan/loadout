import urllib.request, json
BASE='http://127.0.0.1:3000'
r = urllib.request.urlopen(BASE+'/api/setup-status')
print('setup-status:', json.loads(r.read()))
req = urllib.request.Request(BASE+'/api/login', data=json.dumps({'password':'wrong'}).encode(), headers={'Content-Type':'application/json'})
try:
    urllib.request.urlopen(req)
    print('login wrong: unexpected 200')
except urllib.error.HTTPError as e:
    print('login wrong:', e.code, json.loads(e.read()))
