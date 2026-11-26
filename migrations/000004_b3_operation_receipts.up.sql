-- The reservation, document, change fact and completed receipt share one transaction.
CREATE TABLE _al_b3_operation_receipts (
    operation_id TEXT PRIMARY KEY,
    binding_sha256 TEXT NOT NULL,
    target_type TEXT NOT NULL,
    document_id BIGINT NOT NULL CHECK (document_id > 0),
    generation BIGINT NOT NULL CHECK (generation > 0),
    document_revision BIGINT CHECK (document_revision > 0),
    noop BOOLEAN,
    CHECK ((document_revision IS NULL) = (noop IS NULL))
);
