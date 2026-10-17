import re
files = ['plugins/admin-api/context_edit_test.go','plugins/admin-api/models_test.go','plugins/admin-api/stats_test.go','plugins/admin-api/test_proxy_summary_test.go','apps/server/server_test.go']
for f in files:
    src = open(f, encoding='utf-8').read()
    if re.search(r'\bos\.', src):
        print(f, 'still uses os, skip')
        continue
    new = re.sub(r'\n\t"os"', '', src, count=1)
    open(f, 'w', encoding='utf-8', newline='').write(new)
    print(f, 'os import removed' if new != src else 'NO IMPORT FOUND')
