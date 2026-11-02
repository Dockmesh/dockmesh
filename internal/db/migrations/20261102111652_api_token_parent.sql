-- api_token_parent
-- A token minted with another token records that parent, so it is held
-- to the parent's current restriction and stops working with it. Older
-- rows have no parent on record.
ALTER TABLE api_tokens ADD COLUMN parent_token_id INTEGER;
