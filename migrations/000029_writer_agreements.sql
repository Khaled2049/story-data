-- +goose Up
CREATE TABLE writer_agreements (
    user_id text NOT NULL,
    terms_version text NOT NULL,
    privacy_version text NOT NULL,
    attestation_version text NOT NULL,
    accepted_at timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY (user_id, terms_version, privacy_version, attestation_version)
);

-- +goose Down
DROP TABLE writer_agreements;
