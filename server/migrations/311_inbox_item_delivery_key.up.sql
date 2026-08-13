ALTER TABLE inbox_item
    ADD COLUMN IF NOT EXISTS delivery_key TEXT;
