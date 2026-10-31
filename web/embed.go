// Package web 打包前端静态文件
package web

import (
	"embed"
	"io/fs"
)

//go:embed static
var files embed.FS

// Static 是 static 目录的内容
var Static, _ = fs.Sub(files, "static")
