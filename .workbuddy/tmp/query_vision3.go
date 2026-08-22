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

	fmt.Println("=== 视觉识别 attempts + 请求详情 (recent 25) ===")
	r, err := db.Query(`SELECT rr.request_id, rr.requested_model, rr.final_model, rr.started_at, rr.result,
		a.step_no, a.result, a.model, a.channel_id, a.error_message, a.metadata_json, a.duration_ms
		FROM route_attempts a JOIN route_requests rr ON rr.request_id = a.request_id
		WHERE a.action = '视觉识别'
		ORDER BY a.started_at DESC LIMIT 25`)
	if err != nil { fmt.Println("query err:", err); return }
	for r.Next() {
		var rid, reqModel, finalModel, started, reqResult, aResult, aModel, aCh string
		var step, dur int
		var errmsg, meta sql.NullString
		if err := r.Scan(&rid, &reqModel, &finalModel, &started, &reqResult, &step, &aResult, &aModel, &aCh, &errmsg, &meta, &dur); err != nil { fmt.Println("scan err:", err); break }
		fmt.Printf("%s | req=%s | reqModel=%q | final=%q | step=%d | attempt:%s %q @%q | %dms | err=%v | meta=%v\n",
			started, rid, reqModel, finalModel, step, aResult, aModel, aCh, dur, errmsg, meta)
	}
	r.Close()
}
