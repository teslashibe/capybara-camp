-- +goose Up
-- Shared transcripts and requests name the person behind each prompt and
-- approval. Earlier rows keep NULL: their author was not recorded.
ALTER TABLE message_revisions ADD COLUMN author_user_id uuid REFERENCES users(id);
ALTER TABLE prompt_requests ADD COLUMN approved_by_user_id uuid REFERENCES users(id);

-- +goose Down
ALTER TABLE prompt_requests DROP COLUMN approved_by_user_id;
ALTER TABLE message_revisions DROP COLUMN author_user_id;
