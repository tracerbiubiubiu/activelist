-- 数据文档 version 改为全库不复用 revision（ECO-20261104-B07-F03）。
-- 升级前停止所有旧写实例；旧代码仍会 version+1/导入置1，不能混跑。
-- 名称以下划线开头，无法被类型注册白名单占用。
CREATE SEQUENCE public._al_document_revision_seq AS BIGINT NO CYCLE;

DO $$
DECLARE
    type_row RECORD;
    type_max BIGINT;
    revision_max BIGINT := 0;
BEGIN
    -- 禁止迁移扫描期间注册类型；动态表取排他锁后再读最高版本。
    LOCK TABLE data_types IN SHARE ROW EXCLUSIVE MODE;
    -- 旧API每个事务至多将同一行version递增一次。已清空的表虽无MAX，
    -- 客户端仍可能保有历史version；原地升级以全库事务号挡住该历史区间。
    -- 全新库尚无类型，无历史数据版本，保留序列从1开始。
    -- 不覆盖手工改版、旧库逻辑恢复后遗失的事务历史或降级再升级。
    IF EXISTS (SELECT 1 FROM data_types) THEN
        revision_max := txid_current();
    END IF;
    FOR type_row IN SELECT type_name FROM data_types ORDER BY type_name LOOP
        EXECUTE format('LOCK TABLE %I IN ACCESS EXCLUSIVE MODE', type_row.type_name);
        EXECUTE format('SELECT COALESCE(MAX(version), 0) FROM %I', type_row.type_name) INTO type_max;
        revision_max := GREATEST(revision_max, type_max);
        EXECUTE format('ALTER TABLE %I ALTER COLUMN version SET DEFAULT nextval(''public._al_document_revision_seq''::regclass)', type_row.type_name);
    END LOOP;
    IF revision_max > 0 THEN
        PERFORM setval('public._al_document_revision_seq', revision_max, true);
    END IF;
END;
$$;
