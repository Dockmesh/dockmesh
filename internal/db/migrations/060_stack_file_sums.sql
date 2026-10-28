-- Checksums of the stack files a deploy reads, as Dockmesh last wrote
-- them. sum is empty when Dockmesh removed the file.
CREATE TABLE IF NOT EXISTS stack_file_sums (
    stack      TEXT NOT NULL,
    file       TEXT NOT NULL,
    sum        TEXT NOT NULL,
    updated_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
    PRIMARY KEY (stack, file)
);
