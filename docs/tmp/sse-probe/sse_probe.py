#!/usr/bin/env python3
"""观测 /api/processes/stream SSE：先拿 token，再连接，记录收到的事件与断开情况。"""
import json, sys, time, urllib.request, threading

BASE = "http://127.0.0.1:3000"

def get_token():
    req = urllib.request.Request(BASE + "/api/sse-token", method="POST", data=b"{}")
    req.add_header("Content-Type", "application/json")
    with urllib.request.urlopen(req, timeout=5) as r:
        return json.load(r).get("token")

token = get_token()
url = BASE + "/api/processes/stream?ssl_token="  # 占位，下方实际拼接
url = BASE + "/api/processes/stream?sse_token=" + urllib.request.quote(token)

print("连接:", url)
req = urllib.request.Request(url)
start = time.time()
count = 0
types = {}
try:
    with urllib.request.urlopen(req, timeout=60) as r:
        print("HTTP status:", r.status)
        for raw in r:
            line = raw.decode("utf-8", "replace").strip()
            if not line:
                continue
            if not line.startswith("data:"):
                continue
            payload = line[5:].strip()
            try:
                ev = json.loads(payload)
                t = ev.get("type")
                types[t] = types.get(t, 0) + 1
                count += 1
                if count <= 3:
                    print(f"[{time.time()-start:.1f}s] type={t} data_len={len(ev.get('data') or [])}")
                    for p in (ev.get("data") or [])[:3]:
                        print("   ", p.get("id"), p.get("name"), p.get("status"))
            except Exception:
                pass
except Exception as e:
    print("连接/读取异常:", e)
print(f"共收到 {count} 条事件, types={types}, 耗时 {time.time()-start:.1f}s")
