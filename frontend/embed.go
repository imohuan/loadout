// Package frontend 内嵌管理后台前端构建产物（frontend/dist），供 apps/server 单二进制分发。
//
// dist/ 目录由 Vite 构建生成（frontend/package.json build），go:embed 要求目录
// 至少存在一个文件，故仓库保留了真实构建产物；改前端后需重新 pnpm build 再编译服务端。
package frontend

import "embed"

// Dist 前端构建产物（frontend/dist），用 http.FileServer 挂载到管理后台路径。
//
//go:embed all:dist
var Dist embed.FS
