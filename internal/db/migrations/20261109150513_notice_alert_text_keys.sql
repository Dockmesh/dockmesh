-- notice_alert_text_keys
-- Texts are also kept as message key plus arguments, so they can be shown
-- in the reader's language. Rows without a key keep their stored text.
ALTER TABLE notices ADD COLUMN title_i18n TEXT NOT NULL DEFAULT '';
ALTER TABLE notices ADD COLUMN body_i18n TEXT NOT NULL DEFAULT '';
ALTER TABLE alert_history ADD COLUMN message_i18n TEXT NOT NULL DEFAULT '';
