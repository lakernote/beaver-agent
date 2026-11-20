// Package web 负责把 Vite 构建产物嵌进 Go 二进制。
package web

import (
	"embed"
	"io/fs"
)

// 下面的 embed 指令与上一行注释之间必须有空行，且 // 和 go:embed 之间不能有空格。
// all:dist 会把 dist 下包括点文件在内的内容编译进 assets。
//
//go:embed all:dist
var assets embed.FS

// DistFS 把嵌入文件系统的根目录从 dist 下沉一层，使调用方可直接读取 index.html。
func DistFS() (fs.FS, error) {
	return fs.Sub(assets, "dist")
}
