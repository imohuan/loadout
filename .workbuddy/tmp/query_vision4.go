package main

import (
	"database/sql"
	"fmt"
	_ "modernc.org/sqlite"
)

func main() {
	db, err := sql.Open("sqlite", "file:C:/Users/Administrator/.loadout/loadout.db?mode=ro")
	if err != nil { panic(err) }
	defer db.Close()

	// 最近的 attempts 完整查看
	fmt.Println("=== 最近 40 条 attempts ===")
	r, err := db.Query(`SELECT request_id, step_no, action, model, channel_id, result, error_message, metadata_json, started_at, duration_ms
		FROM route_attempts ORDER BY started_at DESC LIMIT 40`)
	if err != nil { fmt.Println("err:", err); return }
	for r.Next() {
		var rid, action, model, ch, result, started string
		var step, dur int
		var errmsg, meta sql.NullString
		if err := r.Scan(&rid, &step, &action, &model, &ch, &result, &errmsg, &meta, &started, &dur); err != nil { fmt.Println("scan err:", err); break }
		fmt.Printf("%s step=%d action=%q model=%q ch=%q res=%q dur=%dms err=%v meta=%v\n", started, step, action, model, ch, result, dur, errmsg, meta)
	}
	r.Close()

	// 含 capability=vision 的 attempts
	fmt.Println("\n=== metadata 含 vision 的 attempts ===")
	r2, _ := db.Query(`SELECT request_id, step_no, action, model, channel_id, result, metadata_json, started_at
		FROM route_attempts WHERE metadata_json LIKE '%vision%' ORDER BY started_at DESC LIMIT 20`)
	for r2.Next() {
		var rid, action, model, ch, result, started, meta string
		var step int
		if err := r2.Scan(&rid, &step, &action, &model, &ch, &result, &meta, &started); err != nil { fmt.Println("scan err:", err); break }
		fmt.Printf("%s step=%d action=%q model=%q ch=%q res=%q meta=%s\n", started, step, action, model, ch, result, meta)
	}
	r2.Close()
}
