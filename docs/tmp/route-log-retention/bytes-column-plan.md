# 给 request_logs 加 bytes 列：把「行大小」从算出来变成记下来

## 目标
写入日志时就把这一行占多少字节算好、存进 `bytes` 列；清理时直接按列求和，
不用再对每行跑 `length()`。

## 为什么
现在的容量清理每次都要：
1. 扫全表对 `length(request_json) + length(response_json)` 求和——大库（十几 GB、
   两万多行）上这一步就是全表扫描；
2. 算出来的还只是**内容体积**，跟文件真实大小差着页头/索引/碎片。差得少时会
   出现「估算说到顶、文件还超」，只能靠 VACUUM 反复收敛（`trimBySize` 里那圈
   循环就是为此存在的）。

有 `bytes` 列之后：
- 清理能直接 `SELECT SUM(bytes)`，甚至能在 `bytes` 上建索引做前缀和，不用扫正文；
- 想更准的话可以把每行的固定开销（rowid、索引项、页头摊销）一起算进去，
  估算值更贴近文件大小，VACUUM 循环的次数就少。

## 要改什么
1. `db.go`：`request_logs` 加 `bytes INTEGER NOT NULL DEFAULT 0`；
   老库（已有表、没这列）走 `ALTER TABLE` 补列，并把历史行回填一次。
2. 写入三处都要维护该列：
   - INSERT 半条（请求快照）：算请求字节；
   - UPDATE 收尾（响应体）：重算总字节；
   - UPDATE 自愈还原：重算总字节。
3. `service.go`：`rowBytesExpr` 换成直接读 `bytes` 列；`trimBySize` 的估算查询
   用 `SUM(bytes)`。
4. 测试：列存在、写入后值正确、收尾后递增、老库迁移能补上、清理仍按 FIFO。

## 字节怎么算
`bytes` = 两个 JSON 的实际字节数 + 每行的固定开销常量（rowid/页头/索引摊销）。
固定开销用一个经验常量（`rowFixedOverhead`），目的不是精确到字节，而是让
`SUM(bytes)` 与文件大小在同一量级、少让 VACUUM 循环兜底。

## 验证
- `go test ./plugins/request-log/... ./core/...`
- 端到端复跑 318MB/5000 行 → 20MB 场景，看是否仍精准停在上限内、且轮次更少。
- 老库兼容：造一个没有 bytes 列的旧库，确认打开时自动补列并回填。
