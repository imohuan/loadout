package db

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"time"

	"loadout/core/procreg"
)

// processHistoryColumns 进程历史表的列（与 migration v26 保持一致）。
const processHistoryColumns = "proc_id, name, kind, cmd, pid, status, started_at, ended_at, exit_code, mem_bytes, log_json"

// ProcessHistoryRepository 持久化 procreg 历史记录到 SQLite process_history 表。
// 实现 procreg.HistoryStore 接口，注入后让历史跨后端重启保留。
type ProcessHistoryRepository struct{ database *sql.DB }

// NewProcessHistoryRepository 绑定进程历史仓储到已打开的数据库。
func NewProcessHistoryRepository(database *sql.DB) (*ProcessHistoryRepository, error) {
	if database == nil {
		return nil, fmt.Errorf("db: nil database")
	}
	// 历史记录为「本次运行会话」的日志：后端（软件）每次重启时清空旧记录，
	// 确保前端历史面板只展示本次运行期间的进程日志。
	if _, err := database.Exec(`DELETE FROM process_history`); err != nil {
		return nil, fmt.Errorf("db: clear process history on startup: %w", err)
	}
	return &ProcessHistoryRepository{database: database}, nil
}

// Save 持久化一条已结束的进程记录（按 proc_id + ended_at upsert）。
// 同一进程 ID 重启后可能复用（proc-N），故以 (proc_id, ended_at) 为去重键，
// 相同 id + 相同结束时间视为同一条（覆盖）；不同结束时间各自保留。
func (r *ProcessHistoryRepository) Save(p procreg.Proc) error {
	logJSON, err := json.Marshal(p.Log)
	if err != nil {
		return fmt.Errorf("db: marshal process log: %w", err)
	}
	endedAt := p.EndedAt.Format(time.RFC3339Nano)
	_, err = r.database.Exec(
		`INSERT INTO process_history (proc_id, name, kind, cmd, pid, status, started_at, ended_at, exit_code, mem_bytes, log_json)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
		 ON CONFLICT(proc_id, ended_at) DO UPDATE SET
		   name=excluded.name, kind=excluded.kind, cmd=excluded.cmd, pid=excluded.pid,
		   status=excluded.status, started_at=excluded.started_at, exit_code=excluded.exit_code,
		   mem_bytes=excluded.mem_bytes, log_json=excluded.log_json`,
		p.ID, p.Name, p.Kind, p.Cmd, p.PID, string(p.Status),
		p.StartedAt.Format(time.RFC3339Nano), endedAt,
		nullableInt(p.ExitCode), p.MemBytes, string(logJSON),
	)
	if err != nil {
		return fmt.Errorf("db: save process history: %w", err)
	}
	return nil
}

// List 返回历史记录，按结束时间新→旧排序。limit<=0 表示不限条数。
func (r *ProcessHistoryRepository) List(limit int) ([]procreg.Proc, error) {
	ctx := context.Background()
	rows, err := r.database.QueryContext(ctx, `
		SELECT proc_id, name, kind, cmd, pid, status, started_at, ended_at, exit_code, mem_bytes, log_json
		FROM process_history
		ORDER BY ended_at DESC`+limitSQL(limit))
	if err != nil {
		return nil, fmt.Errorf("db: query process history: %w", err)
	}
	defer rows.Close()

	out := []procreg.Proc{}
	for rows.Next() {
		var (
			p          procreg.Proc
			pid        int
			status     string
			startedAt  string
			endedAt    string
			exitCode   sql.NullInt64
			memBytes   uint64
			logJSON    string
		)
		if err := rows.Scan(&p.ID, &p.Name, &p.Kind, &p.Cmd, &pid, &status,
			&startedAt, &endedAt, &exitCode, &memBytes, &logJSON); err != nil {
			return nil, fmt.Errorf("db: scan process history: %w", err)
		}
		p.PID = pid
		p.Status = procreg.ProcStatus(status)
		p.StartedAt, _ = time.Parse(time.RFC3339Nano, startedAt)
		p.EndedAt, _ = time.Parse(time.RFC3339Nano, endedAt)
		p.MemBytes = memBytes
		if exitCode.Valid {
			p.ExitCode = int(exitCode.Int64)
		}
		if err := json.Unmarshal([]byte(logJSON), &p.Log); err != nil {
			p.Log = nil
		}
		out = append(out, p)
	}
	return out, rows.Err()
}

// limitSQL 生成 SQLite LIMIT 子句；limit<=0 返回空串（不限）。
func limitSQL(limit int) string {
	if limit > 0 {
		return fmt.Sprintf(" LIMIT %d", limit)
	}
	return ""
}

// nullableInt 把 int 转成可空 sql 值（0 视为 NULL，因为 Go int 无零值语义）。
func nullableInt(v int) any {
	if v == 0 {
		return nil
	}
	return v
}
