-- Set once the cleaner has removed a deleted file's encrypted data from disk.
-- Rows with status 'deleted' and file_removed = 0 still have a file to remove.
ALTER TABLE files ADD COLUMN file_removed INTEGER NOT NULL DEFAULT 0 CHECK (file_removed IN (0, 1));
