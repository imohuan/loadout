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

	fmt.Println("=== channels ===")
	r, _ := db.Query(`SELECT id, name, base_url FROM channels ORDER BY position`)
	for r.Next() {
		var id, name, base string
		if err := r.Scan(&id, &name, &base); err != nil { fmt.Println("scan err:", err); break }
		fmt.Printf("%s | %s | %s\n", id, name, base)
	}
	r.Close()

	fmt.Println("\n=== aggregates ===")
	r2, _ := db.Query(`SELECT id, name, model_pattern FROM aggregates ORDER BY position`)
	for r2.Next() {
		var id, name, pat string
		if err := r2.Scan(&id, &name, &pat); err != nil { fmt.Println("scan err:", err); break }
		fmt.Printf("%s | %s | %s\n", id, name, pat)
	}
	r2.Close()

	fmt.Println("\n=== 8/22 02:29 前后请求详情 ===")
	r3, _ := db.Query(`SELECT request_id, requested_model, virtual_model, final_model, final_channel_id, result, http_status, duration_ms, started_at, error_message
		FROM route_requests WHERE started_at >= '2026-08-22T02:00' ORDER BY started_at DESC LIMIT 15`)
	for r3.Next() {
		var rid, reqM, virM, finM, finCh, result, started string
		var status int
		var dur int64
		var errmsg sql.NullString
		if err := r3.Scan(&rid, &reqM, &virM, &finM, &finCh, &result, &status, &dur, &started, &errmsg); err != nil { fmt.Println("scan err:", err); break }
		fmt.Printf("%s | %s | req=%q vir=%q fin=%q ch=%q | %s(%d) | %dms | err=%v\n", started, rid[:12], reqM, virM, finM, finCh, result, status, dur, errmsg)
	}
	r3.Close()
}
