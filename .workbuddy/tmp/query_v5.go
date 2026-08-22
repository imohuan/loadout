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

	// 17:41-17:42 的 attempts（deepseek-v4-pro-260425 视觉触发时段）
	fmt.Println("=== 8/21 17:41-17:43 attempts ===")
	r, _ := db.Query(`SELECT request_id, step_no, action, model, channel_id, result, duration_ms, started_at, error_message, metadata_json
		FROM route_attempts WHERE started_at BETWEEN '2026-08-21T17:41:00' AND '2026-08-21T17:43:00' ORDER BY started_at`)
	for r.Next() {
		var rid, action, model, ch, result, started string
		var step, dur int
		var errmsg, meta sql.NullString
		if err := r.Scan(&rid, &step, &action, &model, &ch, &result, &dur, &started, &errmsg, &meta); err != nil { fmt.Println("scan err:", err); break }
		fmt.Printf("%s %s step=%d action=%q model=%q ch=%q res=%q dur=%dms err=%v meta=%v\n", started, rid[:12], step, action, model, ch, result, dur, errmsg, meta)
	}
	r.Close()

	// 02:29 的请求+attempts
	fmt.Println("\n=== 8/22 02:29 请求与 attempts ===")
	r2, _ := db.Query(`SELECT request_id, requested_model, final_model, final_channel_id, result, http_status, duration_ms, started_at, error_message
		FROM route_requests WHERE started_at BETWEEN '2026-08-22T02:25:00' AND '2026-08-22T02:35:00' ORDER BY started_at`)
	for r2.Next() {
		var rid, reqM, finM, finCh, result, started string
		var status int
		var dur int64
		var errmsg sql.NullString
		if err := r2.Scan(&rid, &reqM, &finM, &finCh, &result, &status, &dur, &started, &errmsg); err != nil { fmt.Println("scan err:", err); break }
		fmt.Printf("%s %s req=%q fin=%q ch=%q res=%q(%d) %dms err=%v\n", started, rid[:12], reqM, finM, finCh, result, status, dur, errmsg)
	}
	r2.Close()

	r3, _ := db.Query(`SELECT request_id, step_no, action, model, channel_id, result, duration_ms, started_at, error_message
		FROM route_attempts WHERE started_at BETWEEN '2026-08-22T02:25:00' AND '2026-08-22T02:35:00' ORDER BY started_at`)
	fmt.Println("--- attempts ---")
	for r3.Next() {
		var rid, action, model, ch, result, started string
		var step, dur int
		var errmsg sql.NullString
		if err := r3.Scan(&rid, &step, &action, &model, &ch, &result, &dur, &started, &errmsg); err != nil { fmt.Println("scan err:", err); break }
		fmt.Printf("%s %s step=%d action=%q model=%q ch=%q res=%q dur=%dms err=%v\n", started, rid[:12], step, action, model, ch, result, dur, errmsg)
	}
	r3.Close()
}
