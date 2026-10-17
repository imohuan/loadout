import sqlite3, bcrypt
db = 'C:/Users/Administrator/.loadout/loadout.db'
c = sqlite3.connect(db)
row = c.execute('SELECT password_hash, password_changed FROM users WHERE username=?', ('admin',)).fetchone()
print('hash:', row[0])
print('changed:', row[1])
print('match file pw:', bcrypt.checkpw(open('C:/Users/Administrator/.loadout/admin-password','rb').read().strip(), row[0].encode()))
