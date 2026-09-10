Set-Location 'D:\Code\Git\loadout'

git add plugins/unifyai/service.go `
        plugins/admin-api/service.go `
        core/procreg/procreg.go `
        frontend/src/lib/unifyai.ts `
        frontend/src/components/unifyai/UnifyaiPanel.vue `
        plugins/unifyai/source_empty_test.go `
        plugins/unifyai/source_persist_cli_test.go `
        plugins/unifyai/cold_proxy_retry_test.go `
        plugins/unifyai/enable_vision_stale_test.go `
        plugins/unifyai/metadata_enrich_test.go `
        plugins/unifyai/metadata_enrich_real_test.go `
        plugins/unifyai/metadata_enrich_registry_test.go `
        docs/tmp/unifyai-opencodex-zero/research.md

Write-Output '--- staged ---'
git diff --cached --stat

git commit -F 'D:\Code\Git\loadout\docs\tmp\unifyai-opencodex-zero\commit-msg-loadout.txt'

Write-Output '--- log ---'
git log --oneline -3
Write-Output '--- remaining ---'
git status --short
