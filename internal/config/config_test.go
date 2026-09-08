// Package config 配置加载测试：${VAR:-default} 展开 / env 覆盖 / 缺省值 / 非法输入。
package config

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestLoad_ExpandAndDefaults(t *testing.T) {
	t.Setenv("ACTIVELIST_PG_PASSWORD", "s3cret")

	dir := t.TempDir()
	path := filepath.Join(dir, "config.yaml")
	require.NoError(t, os.WriteFile(path, []byte(`
server:
  port: 9090
postgres:
  host: ${ACTIVELIST_PG_HOST:-db.internal}
  password: ${ACTIVELIST_PG_PASSWORD:-should-be-overridden}
  dbname: alx
log:
  level: warn
`), 0o600))

	cfg, err := Load(path)
	require.NoError(t, err)
	require.Equal(t, 9090, cfg.Server.Port)
	// ${VAR:-default}：env 未设置取缺省
	require.Equal(t, "db.internal", cfg.Postgres.Host)
	// ${VAR:-default}：env 已设置取 env
	require.Equal(t, "s3cret", cfg.Postgres.Password)
	require.Equal(t, "alx", cfg.Postgres.DBName)
	// 未写节点取内置缺省
	require.Equal(t, int(30*time.Second), int(cfg.Server.ReadTimeout))
	require.Equal(t, 10, cfg.Postgres.MaxOpenConns)
	require.Equal(t, "warn", cfg.Log.Level)
	require.Equal(t, "logs", cfg.Log.Dir)
}

func TestLoad_EnvOverridesYaml(t *testing.T) {
	t.Setenv("ACTIVELIST_HTTP_PORT", "8888")
	t.Setenv("ACTIVELIST_PG_HOST", "pg-other")

	dir := t.TempDir()
	path := filepath.Join(dir, "config.yaml")
	require.NoError(t, os.WriteFile(path, []byte("server:\n  port: 9090\npostgres:\n  host: db.internal\n"), 0o600))

	cfg, err := Load(path)
	require.NoError(t, err)
	require.Equal(t, 8888, cfg.Server.Port, "env 显式绑定压过 yaml")
	require.Equal(t, "pg-other", cfg.Postgres.Host)
}

func TestLoad_EnvOnly(t *testing.T) {
	t.Setenv("ACTIVELIST_HTTP_PORT", "7070")
	cfg, err := Load(filepath.Join(t.TempDir(), "not-exist.yaml"))
	require.NoError(t, err, "文件不存在 → env-only 模式")
	require.Equal(t, 7070, cfg.Server.Port)
}

func TestLoad_Invalid(t *testing.T) {
	t.Run("端口越界拒绝", func(t *testing.T) {
		t.Setenv("ACTIVELIST_HTTP_PORT", "70000")
		_, err := Load("")
		require.ErrorContains(t, err, "server.port")
	})
	t.Run("坏 yaml 拒绝", func(t *testing.T) {
		path := filepath.Join(t.TempDir(), "config.yaml")
		require.NoError(t, os.WriteFile(path, []byte("server: [broken"), 0o600))
		_, err := Load(path)
		require.Error(t, err)
	})
}

func TestPostgresDSN(t *testing.T) {
	dsn := Postgres{Host: "db", Port: 5432, User: "u", Password: "p@ss:word", DBName: "al"}.DSN()
	require.Contains(t, dsn, "postgres://u:p%40ss%3Aword@db:5432/al", "密码保留字符须转义")
	require.Contains(t, dsn, "sslmode=disable")
}
