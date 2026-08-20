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

	// 表清单
	rows, _ := db.Query("SELECT name FROM sqlite_master WHERE type='table' ORDER BY name")
	fmt.Println("=== TABLES ===")
	for rows.Next() { var n string; rows.Scan(&n); fmt.Println(n) }

	// capability_routes schema
	rows2, _ := db.Query("PRAGMA table_info(capability_routes)")
	fmt.Println("=== capability_routes COLUMNS ===")
	for rows2.Next() { var cid, name, typ, notnull, dflt, pk interface{}; rows2.Scan(&cid, &name, &typ, &notnull, &dflt, &pk); fmt.Printf("%v %v %v\n", name, typ, notnull) }

	// 数据
	rows3, _ := db.Query("SELECT capability, route, models_json, channel_ids_json, via_options_json, replacements_json FROM capability_routes ORDER BY position")
	fmt.Println("=== capability_routes DATA ===")
	for rows3.Next() {
		var cap, route, models, channels, via, repl string
		if err := rows3.Scan(&cap, &route, &models, &channels, &via, &repl); err != nil {
			fmt.Println("scan err:", err); break
		}
		fmt.Printf("capability=%q route=%q\n  models=%s\n  channels=%s\n  via=%s\n  replacements=%s\n", cap, route, models, channels, via, repl)
	}
	if err := rows3.Err(); err != nil { fmt.Println("rows err:", err) }
}
