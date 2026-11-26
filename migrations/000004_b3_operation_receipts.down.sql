-- Preserve replay evidence even when this is the first step of a rollback.
DO $$
BEGIN
    IF EXISTS (SELECT 1 FROM _al_b3_stream) OR EXISTS (SELECT 1 FROM _al_b3_operation_receipts) THEN
        RAISE EXCEPTION 'cannot remove commissioned ActiveList operation receipts';
    END IF;
END $$;
DROP TABLE _al_b3_operation_receipts;
