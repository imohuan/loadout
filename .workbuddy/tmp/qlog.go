package main

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"os"
	"regexp"
	"strings"

	_ "modernc.org/sqlite"
)

func openRO(path string) *sql.DB {
	db, err := sql.Open("sqlite", "file:"+path+"?mode=ro")
	if err != nil {
		panic(err)
	}
	return db
}

func main() {
	db := openRO("C:/Users/Administrator/.loadout/request-log.db")
	defer db.Close()

	switch os.Args[1] {
	case "list":
		// list by request_id prefix
		prefix := os.Args[2]
		rows, err := db.Query(`SELECT id, request_id, model, channel, http_status, stream, started_at, finished_at, duration_ms, result, substr(request_json,1,0), length(request_json), length(coalesce(response_json,'')) FROM request_logs WHERE request_id LIKE ? ORDER BY started_at`, prefix+"%")
		if err != nil {
			panic(err)
		}
		defer rows.Close()
		for rows.Next() {
			var id, reqID, model, channel, started, result string
			var fin, chan2 sql.NullString
			var status, stream, dur, rl, pl sql.NullInt64
			rows.Scan(&id, &reqID, &model, &channel, &status, &stream, &started, &fin, &dur, &result, &chan2, &rl, &pl)
			fmt.Printf("%s | %s | model=%s | ch=%s | status=%v | stream=%v | %s -> %v | dur=%v | %s | reqlen=%v resplen=%v\n", id, reqID, model, channel, status.Int64, stream.Int64, started, fin.String, dur.Int64, result, rl.Int64, pl.Int64)
		}
	case "detail":
		id := os.Args[2]
		var reqJSON, respJSON, model, channel, started, result string
		var fin sql.NullString
		var status, stream, dur sql.NullInt64
		err := db.QueryRow(`SELECT request_json, coalesce(response_json,''), model, channel, started_at, finished_at, duration_ms, result, http_status, stream FROM request_logs WHERE id = ?`, id).Scan(&reqJSON, &respJSON, &model, &channel, &started, &fin, &dur, &result, &status, &stream)
		if err != nil {
			panic(err)
		}
		limit := 20000
		if len(os.Args) > 3 && os.Args[3] == "full" {
			limit = 1 << 30
		}
		fmt.Printf("### id=%s model=%s channel=%s %s -> %v dur=%v result=%s status=%v stream=%v\n", id, model, channel, started, fin.String, dur.Int64, result, status.Int64, stream.Int64)
		fmt.Println("--- REQUEST ---")
		fmt.Println(trim(reqJSON, limit))
		fmt.Println("--- RESPONSE ---")
		fmt.Println(trim(respJSON, limit))
	case "search":
		// search request_json/response_json content
		q := os.Args[2]
		rows, err := db.Query(`SELECT id, request_id, model, channel, started_at, result, coalesce(http_status,0), length(request_json), length(coalesce(response_json,'')) FROM request_logs WHERE request_json LIKE ? OR coalesce(response_json,'') LIKE ? ORDER BY started_at`, "%"+q+"%", "%"+q+"%")
		if err != nil {
			panic(err)
		}
		defer rows.Close()
		n := 0
		for rows.Next() {
			var id, reqID, model, channel, started, result string
			var status, rl, pl int64
			rows.Scan(&id, &reqID, &model, &channel, &started, &result, &status, &rl, &pl)
			fmt.Printf("%s | %s | model=%s | ch=%s | %s | %s | %d | %d/%d\n", id, reqID, model, channel, started, result, status, rl, pl)
			n++
			if n > 400 {
				fmt.Println("... truncated")
				break
			}
		}
	case "models":
		// aggregate model usage in a time window
		from, to := os.Args[2], os.Args[3]
		rows, err := db.Query(`SELECT model, channel, result, count(*), sum(coalesce(duration_ms,0)) FROM request_logs WHERE started_at >= ? AND started_at <= ? GROUP BY model, channel, result ORDER BY 4 DESC`, from, to)
		if err != nil {
			panic(err)
		}
		defer rows.Close()
		for rows.Next() {
			var model, channel, result string
			var c, d int64
			rows.Scan(&model, &channel, &result, &c, &d)
			fmt.Printf("%6d  %-45s %-40s %-20s %dms\n", c, model, channel, result, d)
		}
	case "timeline":
		from, to := os.Args[2], os.Args[3]
		rows, err := db.Query(`SELECT id, request_id, model, channel, started_at, coalesce(finished_at,''), coalesce(http_status,0), result, coalesce(duration_ms,0), length(request_json), length(coalesce(response_json,'')) FROM request_logs WHERE started_at >= ? AND started_at <= ? ORDER BY started_at`, from, to)
		if err != nil {
			panic(err)
		}
		defer rows.Close()
		for rows.Next() {
			var id, reqID, model, channel, started, fin, result string
			var status, dur, rl, pl int64
			rows.Scan(&id, &reqID, &model, &channel, &started, &fin, &status, &result, &dur, &rl, &pl)
			fmt.Printf("%s | %s | %-40s | %-30s | %3d | %-12s | %7dms | %s | rq=%d rs=%d\n", started, reqID, model, channel, status, result, dur, id, rl, pl)
		}
	case "errors":
		from, to := os.Args[2], os.Args[3]
		rows, err := db.Query(`SELECT started_at, request_id, model, channel, coalesce(http_status,0), result, substr(coalesce(response_json,''),1,300) FROM request_logs WHERE started_at >= ? AND started_at <= ? AND (result <> 'success' OR coalesce(http_status,0) >= 400) ORDER BY started_at`, from, to)
		if err != nil {
			panic(err)
		}
		defer rows.Close()
		for rows.Next() {
			var started, reqID, model, channel, result, resp string
			var status int64
			rows.Scan(&started, &reqID, &model, &channel, &status, &result, &resp)
			fmt.Printf("%s | %s | %-40s | %-28s | %3d | %-18s | %s\n", started, reqID, model, channel, status, result, strings.ReplaceAll(strings.ReplaceAll(resp, "\n", " "), "\r", ""))
		}
	case "count":
		var c int64
		db.QueryRow(`SELECT count(*) FROM request_logs`).Scan(&c)
		fmt.Println("total rows:", c)
		var mn, mx sql.NullString
		db.QueryRow(`SELECT min(started_at), max(started_at) FROM request_logs`).Scan(&mn, &mx)
		fmt.Println("range:", mn.String, "->", mx.String)
	case "ids":
		// find rows whose response contains a given identifier (uuid etc)
		q := os.Args[2]
		rows, err := db.Query(`SELECT id, request_id, model, channel, started_at, result FROM request_logs WHERE response_json LIKE ? ORDER BY started_at LIMIT 100`, "%"+q+"%")
		if err != nil {
			panic(err)
		}
		defer rows.Close()
		for rows.Next() {
			var id, reqID, model, channel, started, result string
			rows.Scan(&id, &reqID, &model, &channel, &started, &result)
			fmt.Printf("%s | %s | %s | %s | %s | %s\n", id, reqID, model, channel, started, result)
		}
	case "schema":
		rows, _ := db.Query(`SELECT sql FROM sqlite_master`)
		for rows.Next() {
			var s string
			rows.Scan(&s)
			fmt.Println(s)
		}
	case "jsonkeys":
		id := os.Args[2]
		var reqJSON string
		db.QueryRow(`SELECT request_json FROM request_logs WHERE id=?`, id).Scan(&reqJSON)
		var m map[string]interface{}
		json.Unmarshal([]byte(reqJSON), &m)
		for k := range m {
			fmt.Println(k)
		}
	}
}

func trim(s string, n int) string {
	re := regexp.MustCompile(`\\n`)
	_ = re
	if len(s) > n {
		return s[:n] + "\n...[truncated]"
	}
	return s
}
