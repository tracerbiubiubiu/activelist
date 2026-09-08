// Package migrations 迁移 SQL 的 embed 载体（iofs 供 golang-migrate 读取）。
// SQL 与 zhuzhao 惯例一致置于 migrations/ 顶层，编号全局唯一、up/down 成对。
package migrations

import "embed"

//go:embed *.sql
var FS embed.FS
