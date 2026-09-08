// Package config 配置加载（基线 §6 对齐 taskrunner/zhuzhao 模式）：
// yaml + ${VAR:-default} 环境变量展开 + 显式 env 覆盖绑定（无 yaml 文件可纯 env 运行）。
package config

import (
	"bytes"
	"fmt"
	"os"
	"regexp"
	"time"

	"github.com/spf13/viper"

	utilspostgres "github.com/tracerbiubiubiu/zhuzhao-utils/postgres"
)

type Server struct {
	Port        int           `mapstructure:"port"`
	ReadTimeout time.Duration `mapstructure:"read_timeout"`
	// WriteTimeout 需容纳导入大文件全程（M-A5；默认 300s）
	WriteTimeout time.Duration `mapstructure:"write_timeout"`
}

type Postgres struct {
	Host         string `mapstructure:"host"`
	Port         int    `mapstructure:"port"`
	User         string `mapstructure:"user"`
	Password     string `mapstructure:"password"`
	DBName       string `mapstructure:"dbname"`
	MaxOpenConns int    `mapstructure:"max_open_conns"`
}

// DSN 连接串（utils postgres.Config 构造——密码经 url 转义，env 注入字符不受控）。
func (p Postgres) DSN() string {
	uc := utilspostgres.Config{
		Host: p.Host, Port: p.Port, User: p.User, Password: p.Password,
		DBName: p.DBName, SSLMode: "disable",
	}
	uc.ApplyDefaults()
	return uc.DSN()
}

type Log struct {
	Level string `mapstructure:"level"`
	Dir   string `mapstructure:"dir"`
}

// Business 业务参数（§6；M-A3 分页 / M-A5 导入分批消费，当前仅承载）。
type Business struct {
	PageSizeDefault int `mapstructure:"page_size_default"`
	PageSizeMax     int `mapstructure:"page_size_max"`
	// ImportBatchRows 全量替换导入同事务内的分批行数（百万行级控内存/WAL）。
	ImportBatchRows int `mapstructure:"import_batch_rows"`
}

type Security struct {
	// Callers 验签密钥环（AK→SK，当前唯一调用方 zhuzhao）。中间件随 M-A6 落地，
	// 届时空密钥环拒绝启动（fail-closed，对齐 taskrunner C2）；本里程碑仅承载。
	Callers map[string]string `mapstructure:"callers"`
}

type Config struct {
	Server   Server   `mapstructure:"server"`
	Postgres Postgres `mapstructure:"postgres"`
	Log      Log      `mapstructure:"log"`
	Business Business `mapstructure:"business"`
	Security Security `mapstructure:"security"`
}

// envVarRe 支持 ${VAR} 与 ${VAR:-default} 两种形态（实现计划 §6 yaml 语法）。
var envVarRe = regexp.MustCompile(`\$\{([A-Za-z_][A-Za-z0-9_]*)(?::-([^}]*))?\}`)

func expandEnv(b []byte) []byte {
	return envVarRe.ReplaceAllFunc(b, func(m []byte) []byte {
		sub := envVarRe.FindSubmatch(m)
		if v, ok := os.LookupEnv(string(sub[1])); ok {
			return []byte(v)
		}
		if len(sub) > 2 {
			return sub[2] // 未设置时取 :- 缺省
		}
		return nil // ${VAR} 未设置且无缺省 → 空串
	})
}

// Load 从 path 读 yaml（${VAR} 展开后解析）；文件不存在 → env-only 模式。
// 显式 BindEnv 使 ACTIVELIST_* 环境变量始终压过 yaml（容器注入敏感值）。
func Load(path string) (*Config, error) {
	v := viper.New()
	if path != "" {
		if raw, err := os.ReadFile(path); err == nil {
			v.SetConfigType("yaml")
			if err := v.ReadConfig(bytes.NewReader(expandEnv(raw))); err != nil {
				return nil, fmt.Errorf("read config: %w", err)
			}
		} else if !os.IsNotExist(err) {
			return nil, fmt.Errorf("stat config: %w", err)
		}
	}

	bind := func(key, env string, def any) {
		v.BindEnv(key, env)
		if !v.IsSet(key) || v.GetString(key) == "" {
			v.SetDefault(key, def)
		}
	}
	bind("server.port", "ACTIVELIST_HTTP_PORT", 8080)
	bind("server.read_timeout", "ACTIVELIST_HTTP_READ_TIMEOUT", "30s")
	bind("server.write_timeout", "ACTIVELIST_HTTP_WRITE_TIMEOUT", "300s")
	bind("postgres.host", "ACTIVELIST_PG_HOST", "127.0.0.1")
	viperBindInt(v, "postgres.port", "ACTIVELIST_PG_PORT", 5432)
	bind("postgres.user", "ACTIVELIST_PG_USER", "activelist")
	bind("postgres.password", "ACTIVELIST_PG_PASSWORD", "activelist")
	bind("postgres.dbname", "ACTIVELIST_PG_DBNAME", "activelist")
	viperBindInt(v, "postgres.max_open_conns", "ACTIVELIST_PG_MAX_OPEN_CONNS", 10)
	bind("log.level", "ACTIVELIST_LOG_LEVEL", "info")
	bind("log.dir", "ACTIVELIST_LOG_DIR", "logs")
	viperBindInt(v, "business.page_size_default", "ACTIVELIST_BUSINESS_PAGE_SIZE_DEFAULT", 20)
	viperBindInt(v, "business.page_size_max", "ACTIVELIST_BUSINESS_PAGE_SIZE_MAX", 100)
	viperBindInt(v, "business.import_batch_rows", "ACTIVELIST_BUSINESS_IMPORT_BATCH_ROWS", 1000)
	v.BindEnv("security.callers.zhuzhao", "ACTIVELIST_CALLER_ZHUZHAO_SK")

	var cfg Config
	if err := v.Unmarshal(&cfg); err != nil {
		return nil, fmt.Errorf("unmarshal config: %w", err)
	}
	if cfg.Server.Port <= 0 || cfg.Server.Port > 65535 {
		return nil, fmt.Errorf("server.port 非法: %d", cfg.Server.Port)
	}
	if cfg.Postgres.Host == "" || cfg.Postgres.DBName == "" {
		return nil, fmt.Errorf("postgres.host/dbname 不能为空")
	}
	if cfg.Business.PageSizeDefault <= 0 || cfg.Business.PageSizeMax < cfg.Business.PageSizeDefault ||
		cfg.Business.ImportBatchRows <= 0 {
		return nil, fmt.Errorf("business 参数非法（须为正且 page_size_max ≥ page_size_default）")
	}
	if cfg.Security.Callers == nil {
		cfg.Security.Callers = map[string]string{}
	}
	if sk := v.GetString("security.callers.zhuzhao"); sk != "" {
		cfg.Security.Callers["zhuzhao"] = sk
	}
	return &cfg, nil
}

// viperBindInt 整型 env 绑定（GetString 判空对 "0" 会误设默认，单独处理）。
func viperBindInt(v *viper.Viper, key, env string, def int) {
	v.BindEnv(key, env)
	if !v.IsSet(key) {
		v.SetDefault(key, def)
	}
}
