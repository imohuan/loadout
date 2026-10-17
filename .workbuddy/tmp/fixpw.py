import re
files = ['plugins/admin-api/context_edit_test.go','plugins/admin-api/models_test.go','plugins/admin-api/stats_test.go','plugins/admin-api/test_proxy_summary_test.go']
pattern = re.compile(
    r'pw, err := os\.ReadFile\(config\.AdminPasswordFile\)\s*\n\s*if err != nil \{\s*\n\s*t\.Fatalf\([^\n]*\)\s*\n\s*\}'
)
for f in files:
    src = open(f, encoding='utf-8').read()
    new = pattern.sub('pw := "test-access-key"\n\t_ = pw', src)
    open(f, 'w', encoding='utf-8', newline='').write(new)
    print(f, 'replaced' if new != src else 'NO MATCH')
