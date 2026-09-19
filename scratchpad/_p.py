import pathlib
p = pathlib.Path(r'D:/Code/Git/loadout/plugins/admin-api/service.go')
src = p.read_text(encoding="utf-8")
anchor = "deps.UseGlobal = req.UseGlobalCmd // 开关即时生效（unifyai/skills 后续执行按此选命令）"
add = anchor + "\n\tif s.health != nil {\n\t\ts.health.SetRuleAIModel(req.RuleAIModel) // 失败规则引擎 AI 兜底热更新\n\t}"
if "SetRuleAIModel" not in src:
    src = src.replace(anchor, add, 1)
    p.write_text(src, encoding="utf-8")
    print("patched")
else:
    print("already")
