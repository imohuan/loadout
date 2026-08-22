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

	// 视觉识别 attempt 关联请求详情
	fmt.Println("=== 视觉识别 attempts + 请求详情 (recent 25) ===")
	r, _ := db.Query(`SELECT rr.request_id, rr.model, rr.channel_id, rr.started_at, rr.status, rr.duration_ms,
		a.step_no, a.action, a.result, a.error_message, a.metadata_json
		FROM route_attempts a JOIN route_requests rr ON rr.request_id = a.request_id
		WHERE a.action = '视觉识别'
		ORDER BY a.started_at DESC LIMIT 25`)
	for r.Next() {
		var rid, model, chid, started, status, action, result string
		var step int; var dur int64
		var errmsg, meta sql.NullString
		if err := r.Scan(&rid, &model, &chid, &started, &status, &dur, &step, &action, &result, &errmsg, &meta); err != nil { fmt.Println("scan err:", err); break }
		fmt.Printf("%s | req=%s | model=%q | step=%d | %s | ch=%q | dur=%dms | err=%v | meta=%v\n",
			started, rid, model, step, result, chid, dur, errmsg, meta)
	}
	r.Close()
}
