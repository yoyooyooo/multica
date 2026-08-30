CREATE UNIQUE INDEX CONCURRENTLY IF NOT EXISTS inbox_item_delivery_key_uidx
    ON inbox_item (workspace_id, recipient_type, recipient_id, delivery_key)
    WHERE delivery_key IS NOT NULL;
