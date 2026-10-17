package main

import (
	"database/sql"
	"fmt"
	"os"
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
	db := openRO("C:/Users/Administrator/.loadout/loadout.db")
	defer db.Close()

	switch os.Args[1] {
	case "tables":
		rows, _ := db.Query(`SELECT name FROM sqlite_master WHERE type='table' ORDER BY name`)
		for rows.Next() {
			var n string
			rows.Scan(&n)
			fmt.Println(n)
		}
	case "schema":
		rows, _ := db.Query(`SELECT sql FROM sqlite_master WHERE name LIKE ?`)
		for rows.Next() {
			var s sql.NullString
			rows.Scan(&s)
			fmt.Println(s.String)
			fmt.Println("---")
		}
	case "cols":
		rows, _ := db.Query(`SELECT name, type FROM pragma_table_info(?)`, os.Args[2])
		for rows.Next() {
			var n, t string
			rows.Scan(&n, &t)
			fmt.Printf("%-30s %s\n", n, t)
		}
	case "req":
		// route_requests by request_id
		rows, err := db.Query(`SELECT * FROM route_requests WHERE request_id LIKE ? ORDER BY rowid`, os.Args[2]+"%")
		if err != nil {
			panic(err)
		}
		cols, _ := rows.Columns()
		fmt.Println(strings.Join(cols, " | "))
		for rows.Next() {
			vals := make([]interface{}, len(cols))
			ptrs := make([]interface{}, len(cols))
			for i := range vals {
				ptrs[i] = &vals[i]
			}
			rows.Scan(ptrs...)
			parts := make([]string, len(cols))
			for i, v := range vals {
				s := fmt.Sprintf("%v", v)
				if len(s) > 300 {
					s = s[:300] + "..."
				}
				parts[i] = s
			}
			fmt.Println(strings.Join(parts, " | "))
		}
	case "rawsql":
		rows, err := db.Query(os.Args[2])
		if err != nil {
			panic(err)
		}
		defer rows.Close()
		cols, _ := rows.Columns()
		fmt.Println(strings.Join(cols, " | "))
		n := 0
		for rows.Next() {
			vals := make([]interface{}, len(cols))
			ptrs := make([]interface{}, len(cols))
			for i := range vals {
				ptrs[i] = &vals[i]
			}
			rows.Scan(ptrs...)
			parts := make([]string, len(cols))
			for i, v := range vals {
				s := fmt.Sprintf("%v", v)
				if len(s) > 4000 {
					s = s[:4000] + "...[trunc]"
				}
				parts[i] = s
			}
			fmt.Println(strings.Join(parts, " | "))
			n++
			if n > 2000 {
				fmt.Println("...truncated")
				break
			}
		}
	}
}
