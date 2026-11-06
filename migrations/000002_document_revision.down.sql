-- 回滚必须同批退回旧代码，并停止新写实例；新代码依赖 revision sequence。
-- 已发出的版本值不改写；恢复旧的后续分配语义（不再承诺跨导入防ABA）。
DO $$
DECLARE
    type_row RECORD;
BEGIN
    LOCK TABLE data_types IN SHARE ROW EXCLUSIVE MODE;
    FOR type_row IN SELECT type_name FROM data_types ORDER BY type_name LOOP
        EXECUTE format('ALTER TABLE %I ALTER COLUMN version SET DEFAULT 1', type_row.type_name);
    END LOOP;
END;
$$;
DROP SEQUENCE public._al_document_revision_seq;
