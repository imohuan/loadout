// 本地开发辅助：用仓库自身的 auth 包签发一个管理员会话 token，
// 供自动化/调试直接以已登录身份访问本地服务（免去手工登录）。
// 仅使用本机 ~/.loadout/data/.secret 作为签名密钥，不产生外部副作用。
package main

import (
	"fmt"
	"os"
	"path/filepath"
	"time"

	"loadout/core/auth"
)

func main() {
	home, err := os.UserHomeDir()
	if err != nil {
		fmt.Fprintln(os.Stderr, "home:", err)
		os.Exit(1)
	}
	secret, err := os.ReadFile(filepath.Join(home, ".loadout", "data", ".secret"))
	if err != nil {
		fmt.Fprintln(os.Stderr, "read secret:", err)
		os.Exit(1)
	}
	user := "admin"
	if len(os.Args) > 1 {
		user = os.Args[1]
	}
	tok, err := auth.SignToken(secret, user, 24*time.Hour)
	if err != nil {
		fmt.Fprintln(os.Stderr, "sign:", err)
		os.Exit(1)
	}
	fmt.Print(tok)
}
