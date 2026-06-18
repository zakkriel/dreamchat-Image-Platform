-- +goose Up
CREATE TABLE goose_canary (id integer PRIMARY KEY);

-- +goose Down
DROP TABLE goose_canary;
