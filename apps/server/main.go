// Loadout 服务器入口。
package main

import (
	"fmt"
	"os"
)

func main() {
	if err := Run(); err != nil {
		fmt.Fprintln(os.Stderr, "loadout:", err)
		os.Exit(1)
	}
}
