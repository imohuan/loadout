import sqlite3, json, os, subprocess
con = sqlite3.connect('C:/Users/Administrator/.loadout/loadout.db')
con.row_factory = sqlite3.Row
req = 'f337b8e471d728bf'
for a in con.execute("SELECT step_no, action, model, channel_id, channel_name, metadata_json FROM route_attempts WHERE request_id=? AND step_no='4.1'", (req,)):
    md = json.loads(a['metadata_json'] or '{}')
    print('4.1 metadata:', md)
for a in con.execute("SELECT step_no, action, channel_id, channel_name, metadata_json FROM route_attempts WHERE request_id=? AND step_no='4'", (req,)):
    md = json.loads(a['metadata_json'] or '{}')
    print('4 metadata:', md)
out = subprocess.run(['tasklist', '/FI', 'IMAGENAME eq loadout.exe'], capture_output=True, text=True)
print('=== tasklist ===')
print(out.stdout)
for p in ['C:/Code/Git/loadout/bin/loadout.exe', 'C:/Users/Administrator/.workbuddy/bin/loadout.exe']:
    if os.path.exists(p):
        st = os.stat(p)
        print(f'{p}: size={st.st_size} mtime={st.st_mtime}')
