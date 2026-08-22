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

	tables := []string{"route_requests", "route_attempts"}
	for _, t := range tables {
		rows, err := db.Query("PRAGMA table_info(" + t + ")")
		if err != nil { fmt.Println(t, "err:", err); continue }
		fmt.Println("===", t, "===")
		for rows.Next() {
			var cid, name, typ, notnull, dflt, pk interface{}
			rows.Scan(&cid, &name, &typ, &notnull, &dflt, &pk)
			fmt.Printf("  %v %v\n", name, typ)
		}
		rows.Close()
	}
}
