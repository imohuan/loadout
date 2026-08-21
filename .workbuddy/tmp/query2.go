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
	rows, _ := db.Query("SELECT capability, route, models_json, channel_ids_json, COALESCE(via_options_json,'<NULL>'), COALESCE(replacements_json,'<NULL>') FROM capability_routes ORDER BY position")
	for rows.Next() {
		var cap, route, models, channels, via, repl string
		rows.Scan(&cap, &route, &models, &channels, &via, &repl)
		fmt.Printf("[%s|%s] models=%s channels=%s\n  via=%q\n  replacements=%q\n", cap, route, models, channels, via, repl)
	}
}
