-- saml_provider_options
ALTER TABLE saml_providers ADD COLUMN idp_metadata_url TEXT NOT NULL DEFAULT '';
ALTER TABLE saml_providers ADD COLUMN metadata_fetched_at TIMESTAMP;
ALTER TABLE saml_providers ADD COLUMN metadata_error TEXT NOT NULL DEFAULT '';
ALTER TABLE saml_providers ADD COLUMN sp_entity_id TEXT NOT NULL DEFAULT '';
ALTER TABLE saml_providers ADD COLUMN signing_cert_pem TEXT NOT NULL DEFAULT '';
ALTER TABLE saml_providers ADD COLUMN encryption_cert_pem TEXT NOT NULL DEFAULT '';
ALTER TABLE saml_providers ADD COLUMN encryption_key TEXT NOT NULL DEFAULT '';
