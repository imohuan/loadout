// Package web 内嵌管理后台前端构建产物，供 apps/server 单二进制分发。
//
// dist/ 目录由 Vite 构建生成（见 web/package.json），go:embed 要求目录至少存在
// 一个文件，故仓库里保留了占位 index.html；真实构建时会被覆盖。
package web

import "embed"

// Dist 前端构建产物（web/dist），用 http.FileServer 挂载到管理后台路径。
//
//go:embed dist
var Dist embed.FS
