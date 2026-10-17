import urllib.request, json, io, uuid, http.cookiejar
BASE='http://127.0.0.1:3000'
cj = http.cookiejar.CookieJar()
op = urllib.request.build_opener(urllib.request.HTTPCookieProcessor(cj))
req = urllib.request.Request(BASE+'/api/login', data=json.dumps({'username':'admin','password':'S)6TiKV_Dz(^9eWb'}).encode(), headers={'Content-Type':'application/json'})
op.open(req)
zip_bytes = open('C:/Users/Administrator/Desktop/loadout-config-20261015-072311.zip','rb').read()
# multipart field 'file' with UTF-8 filename as browser does
boundary = uuid.uuid4().hex
body = io.BytesIO()
body.write(f'--{boundary}\r\n'.encode())
body.write('Content-Disposition: form-data; name="file"; filename="loadout-config-20261015-072311.zip"\r\n'.encode('utf-8'))
body.write(b'Content-Type: application/zip\r\n\r\n')
body.write(zip_bytes)
body.write(f'\r\n--{boundary}--\r\n'.encode())
req = urllib.request.Request(BASE+'/api/config/import/preview', data=body.getvalue(), headers={'Content-Type': f'multipart/form-data; boundary={boundary}'})
r = op.open(req)
print('preview:', r.status)
