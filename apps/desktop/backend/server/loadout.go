// Package server 管理 Loadout Server 的生命周期。
package server

import (
	"log"
	"sync"

	"loadout/core/servercore"
)

var (
	serverDone chan error
	mu         sync.Mutex
)

// StartLoadoutServer 在独立 goroutine 中启动 Loadout Server。
func StartLoadoutServer() error {
	mu.Lock()
	defer mu.Unlock()

	if serverDone != nil {
		return nil // 已启动
	}

	serverDone = make(chan error, 1)

	go func() {
		log.Println("正在启动 Loadout Server...")
		if err := servercore.Run(); err != nil {
			log.Printf("Loadout Server 错误: %v", err)
			serverDone <- err
		}
		close(serverDone)
	}()

	log.Println("Loadout Server 已启动")
	return nil
}

// StopLoadoutServer 停止 Loadout Server（由于 Run() 会阻塞直到收到信号，这里只是标记）。
func StopLoadoutServer() {
	mu.Lock()
	defer mu.Unlock()

	if serverDone != nil {
		log.Println("Loadout Server 将在应用退出时停止")
		serverDone = nil
	}
}

