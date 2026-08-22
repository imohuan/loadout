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

	// capability_routes 数据
	rows, _ := db.Query("SELECT capability, route, models_json, channel_ids_json, via_options_json, position FROM capability_routes ORDER BY position")
	fmt.Println("=== capability_routes DATA ===")
	for rows.Next() {
		var cap, route, models, channels, via string
		var pos int
		if err := rows.Scan(&cap, &route, &models, &channels, &via, &pos); err != nil { fmt.Println("scan err:", err); break }
		fmt.Printf("[%d] capability=%q route=%q models=%s channels=%s via=%s\n", pos, cap, route, models, channels, via)
	}
	rows.Close()

	// 视觉识别 attempt 记录（最近 30 条）
	fmt.Println("\n=== route_attempts 视觉识别 (recent 30) ===")
	r2, _ := db.Query(`SELECT r.request_id, r.step_no, r.model, r.channel_id, r.result, r.error_message, r.started_at, r.duration_ms
		FROM route_attempts r ORDER BY r.started_at DESC LIMIT 30`)
	for r2.Next() {
		var rid string; var step, dur int; var model, chid, result, errmsg, started string
		if err := r2.Scan(&rid, &step, &model, &chid, &result, &errmsg, &started, &dur); err != nil { fmt.Println("scan err:", err); break }
		fmt.Printf("%s step=%d model=%q channel=%q result=%q err=%q dur=%dms\n", started, step, model, chid, result, errmsg, dur)
	}
	r2.Close()

	// 统计：视觉识别次数
	fmt.Println("\n=== 视觉识别统计 ===")
	r3, _ := db.Query(`SELECT action, result, COUNT(*), MIN(started_at), MAX(started_at) FROM route_attempts GROUP BY action, result ORDER BY 3 DESC`)
	for r3.Next() {
		var action, result, min, max string; var cnt int
		if err := r3.Scan(&action, &result, &cnt, &min, &max); err != nil { fmt.Println("scan err:", err); break }
		fmt.Printf("action=%q result=%q count=%d (%s ~ %s)\n", action, result, cnt, min, max)
	}
	r3.Close()

	// 最近请求的 metadata（vision 相关）
	fmt.Println("\n=== 最近请求含 vision attempt 的 metadata ===")
	r4, _ := db.Query(`SELECT request_id, model, channel_id, metadata_json, started_at FROM route_requests ORDER BY started_at DESC LIMIT 10`)
	for r4.Next() {
		var rid, model, chid, meta, started string
		if err := r4.Scan(&rid, &model, &chid, &meta, &started); err != nil { fmt.Println("scan err:", err); break }
		fmt.Printf("%s model=%q channel=%q meta=%s\n", started, model, chid, meta)
	}
	r4.Close()
}
