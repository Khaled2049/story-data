package store

import (
	"context"
	"time"
)

// Bump these when the corresponding published documents or attestations change.
const TermsVersion = "2026-10-07"
const PrivacyVersion = "2026-10-07"
const AttestationVersion = "2026-10-07"

const WriterAttestation = "I own my contributions or have the permissions or other lawful basis needed to use them, including imported text and cover images. I will not plagiarize, misrepresent someone else's work as my own, or upload content that infringes copyright, privacy, or other rights. I will review AI output before using or publishing it."

type WriterAgreement struct {
	TermsVersion       string     `json:"termsVersion"`
	PrivacyVersion     string     `json:"privacyVersion"`
	AttestationVersion string     `json:"attestationVersion"`
	Attestation        string     `json:"attestation"`
	Accepted           bool       `json:"accepted"`
	AcceptedAt         *time.Time `json:"acceptedAt,omitempty"`
}

type WriterAgreementInput struct {
	TermsVersion       string `json:"termsVersion"`
	PrivacyVersion     string `json:"privacyVersion"`
	AttestationVersion string `json:"attestationVersion"`
	AgreeTerms         bool   `json:"agreeTerms"`
	AcknowledgePrivacy bool   `json:"acknowledgePrivacy"`
	AttestRights       bool   `json:"attestRights"`
	Adult              bool   `json:"adult"`
}

func (s *Store) GetWriterAgreement(ctx context.Context, uid string) (WriterAgreement, error) {
	out := WriterAgreement{TermsVersion: TermsVersion, PrivacyVersion: PrivacyVersion,
		AttestationVersion: AttestationVersion, Attestation: WriterAttestation}
	err := s.db.QueryRow(ctx, `SELECT max(accepted_at) FROM writer_agreements
        WHERE user_id=$1 AND terms_version=$2 AND privacy_version=$3 AND attestation_version=$4`,
		uid, TermsVersion, PrivacyVersion, AttestationVersion).Scan(&out.AcceptedAt)
	out.Accepted = out.AcceptedAt != nil
	return out, err
}

func (s *Store) AcceptWriterAgreement(ctx context.Context, uid string, in WriterAgreementInput) (WriterAgreement, error) {
	if in.TermsVersion != TermsVersion || in.PrivacyVersion != PrivacyVersion || in.AttestationVersion != AttestationVersion ||
		!in.AgreeTerms || !in.AcknowledgePrivacy || !in.AttestRights || !in.Adult {
		return WriterAgreement{}, sentinelErrf(ErrValidation, "accept the current terms, acknowledge the privacy policy, and confirm your age and content rights")
	}
	_, err := s.db.Exec(ctx, `INSERT INTO writer_agreements (user_id, terms_version, privacy_version, attestation_version)
        VALUES ($1,$2,$3,$4) ON CONFLICT DO NOTHING`, uid, TermsVersion, PrivacyVersion, AttestationVersion)
	if err != nil {
		return WriterAgreement{}, err
	}
	return s.GetWriterAgreement(ctx, uid)
}

func (s *Store) RequireWriterAgreement(ctx context.Context, uid string) error {
	agreement, err := s.GetWriterAgreement(ctx, uid)
	if err != nil {
		return err
	}
	if !agreement.Accepted {
		return sentinelErrf(ErrForbidden, "accept the current writer agreement before writing; visit the story editor to review it")
	}
	return nil
}
