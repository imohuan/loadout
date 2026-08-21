package main

import (
	"database/sql"
	"fmt"
	_ "modernc.org/sqlite"
)

func main() {
	db, _ := sql.Open("sqlite", "C:/Users/Administrator/.loadout/loadout.db")
	defer db.Close()
	rows, _ := db.Query("SELECT version, name, substr(checksum,1,20) FROM schema_migrations ORDER BY version DESC LIMIT 8")
	for rows.Next() {
		var v int
		var n, c string
		rows.Scan(&v, &n, &c)
		fmt.Printf("v%-3d %-40s %s\n", v, n, c)
	}
}
