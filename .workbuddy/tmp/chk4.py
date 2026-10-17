import urllib.request, json, io, uuid, http.cookiejar
BASE='http://127.0.0.1:3000'
cj = http.cookiejar.CookieJar()
op = urllib.request.build_opener(urllib.request.HTTPCookieProcessor(cj))
req = urllib.request.Request(BASE+'/api/login', data=json.dumps({'username':'admin','password':'S)6TiKV_Dz(^9eWb'}).encode(), headers={'Content-Type':'application/json'})
op.open(req)
zip_bytes = open('C:/Users/Administrator/Desktop/loadout-config-20261015-072311.zip','rb').read()
# 有意构造大 multipart header 模拟前端（比如 chunk 边界）
boundary = '----WebKitFormBoundary' + uuid.uuid4().hex[:16]
body = io.BytesIO()
body.write(f'--{boundary}\r\n'.encode())
body.write('Content-Disposition: form-data; name="file"; filename="loadout-config-20261015-072311.zip"\r\n'.encode())
body.write(b'Content-Type: application/zip\r\n')
body.write(b'Content-Transfer-Encoding: binary\r\n\r\n')
body.write(zip_bytes)
body.write(f'\r\n--{boundary}--\r\n'.encode())
req = urllib.request.Request(BASE+'/api/config/import/preview', data=body.getvalue(), headers={'Content-Type': f'multipart/form-data; boundary={boundary}'})
r = op.open(req)
print('with extra header:', r.status)
# 再试 chunked
import socket
print('done')
