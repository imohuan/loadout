import re, io
files = ['plugins/admin-api/admin_api_test.go','plugins/admin-api/context_edit_test.go','plugins/admin-api/models_test.go','plugins/admin-api/stats_test.go','plugins/admin-api/test_proxy_summary_test.go','plugins/admin-api/transfer_test.go']
pattern = re.compile(
    r'if _, err := authSvc\.EnsureFirstRun\(\); err != nil \{\s*\n\s*t\.Fatalf\("EnsureFirstRun: %v", err\)\s*\n\s*\}'
)
for f in files:
    src = open(f, encoding='utf-8').read()
    new = pattern.sub('if err := authSvc.SetupAdminKey("test-access-key"); err != nil {\n\t\tt.Fatalf("SetupAdminKey: %v", err)\n\t}', src)
    open(f, 'w', encoding='utf-8', newline='').write(new)
    print(f, 'replaced' if new != src else 'NO MATCH')
