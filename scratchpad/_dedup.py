import re, pathlib
p = pathlib.Path(r"D:/Code/Git/loadout/plugins/failure-rules/store.go")
src = p.read_text(encoding="utf-8")
# 删除从第二个 "// RuleDecision" 开始到 "// ListDecisions" 第二个实现结束前的重复（第一个保留）
marker = "// RuleDecision 一条判定日志（前端展示用）。"
first = src.index(marker)
second = src.find(marker, first + 1)
if second > 0:
    end = src.index("func (s *Store) RecordDecision", second)
    src = src[:second] + src[end:]
p.write_text(src, encoding="utf-8")
print("dedup ok")
