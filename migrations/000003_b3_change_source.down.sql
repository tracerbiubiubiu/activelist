-- A migration rollback may only remove unused B3 storage.
DO $$
BEGIN
    IF EXISTS (SELECT 1 FROM _al_b3_stream) THEN
        RAISE EXCEPTION 'cannot remove a commissioned ActiveList change stream';
    END IF;
END $$;
DROP TABLE _al_b3_outbox;
DROP TABLE _al_b3_stream;
