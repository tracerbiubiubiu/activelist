-- 000001: 类型元数据表（M-A1 建表，M-A2 类型注册消费）。
-- 设计 = implementation-plan §7 + ADR-003：方案 D 单一当前版本；
-- 每类型数据表由注册流程动态 CREATE（repository 层，不走迁移）。

-- 类型注册：typeName 唯一（并发注册后到者 409）；当前 schema 全量定义；
-- status = active | deprecated（deprecated 拒绝写入）；version 为元数据行
-- 乐观锁（并发 Schema 演进后到者 409，须重读后重提）。
CREATE TABLE IF NOT EXISTS data_types (
    id         BIGSERIAL PRIMARY KEY,
    type_name  VARCHAR(63) NOT NULL,
    schema_def JSONB NOT NULL,
    status     VARCHAR(16) NOT NULL DEFAULT 'active',
    version    BIGINT NOT NULL DEFAULT 1,
    created_by VARCHAR(64) NOT NULL DEFAULT 'system', -- X-Operator 断言（M-A6 起真实值）
    updated_by VARCHAR(64) NOT NULL DEFAULT 'system',
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE UNIQUE INDEX IF NOT EXISTS uq_data_types_name ON data_types (type_name);

-- Schema 变更历史（GET /admin/types/:name/history 素材；只追加不更新，
-- 每行存变更后完整 schema——全量定义、非字段级 merge）。
CREATE TABLE IF NOT EXISTS data_type_schema_history (
    id         BIGSERIAL PRIMARY KEY,
    type_name  VARCHAR(63) NOT NULL,
    op         VARCHAR(16) NOT NULL, -- register | evolve | deprecate
    schema_def JSONB NOT NULL,
    changed_by VARCHAR(64) NOT NULL DEFAULT 'system',
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE INDEX IF NOT EXISTS idx_dtsh_name_created
    ON data_type_schema_history (type_name, created_at DESC);
