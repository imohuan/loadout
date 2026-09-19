import sqlite3, json, sys

db = sqlite3.connect(r"file:C:/Users/Administrator/.loadout/loadout.db?mode=ro", uri=True)
db.row_factory = sqlite3.Row
cur = db.cursor()
tables = [r[0] for r in cur.execute("SELECT name FROM sqlite_master WHERE type='table'").fetchall()]
out = {"tables": tables}
for t in ("model_states", "channel_states"):
    if t in tables:
        rows = cur.execute(f"SELECT * FROM {t} ORDER BY updated_at DESC LIMIT 20").fetchall()
        out[t] = [dict(r) for r in rows]
        out[t + "_summary"] = [dict(r) for r in cur.execute(f"SELECT status, COUNT(*) c FROM {t} GROUP BY status").fetchall()]
print(json.dumps(out, ensure_ascii=False, indent=1, default=str))

# route_attempts 失败分布
cols = [r[1] for r in cur.execute("PRAGMA table_info(route_attempts)").fetchall()]
print("route_attempts columns:", cols)
rows = cur.execute("""
    SELECT model, channel_id, status_code, substr(error_message,1,100) err, COUNT(*) c, MAX(started_at) last_at
    FROM route_attempts
    WHERE result != 'success' AND status_code IS NOT NULL
    GROUP BY model, channel_id, substr(error_message,1,80), status_code
    ORDER BY c DESC LIMIT 15
""" ).fetchall()
for r in rows:
    print(dict(r))

# 每小时失败次数趋势（最近48小时）
trend = cur.execute("""
    SELECT substr(created_at, 1, 13) h, COUNT(*) c
    FROM route_attempts WHERE success = 0
    GROUP BY h ORDER BY h DESC LIMIT 48
""" ).fetchall()
print("hourly failures (desc):", [(dict(r)) for r in trend])
db.close()
