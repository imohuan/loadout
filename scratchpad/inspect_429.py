import sqlite3, sys, io

sys.stdout = io.TextIOWrapper(sys.stdout.buffer, encoding="utf-8")

db = sqlite3.connect(r"file:C:/Users/Administrator/.loadout/loadout.db?mode=ro", uri=True)
cur = db.cursor()
rows = cur.execute(
    "SELECT substr(error_body,1,300), COUNT(*) FROM route_attempts "
    "WHERE result!='success' AND status_code=429 "
    "GROUP BY substr(error_body,1,200) ORDER BY 2 DESC LIMIT 8"
).fetchall()
for e, c in rows:
    print(c, "|", repr(e))
db.close()
