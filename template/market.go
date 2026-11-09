package template

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"strconv"
)

// SourceExpectation binds acquisition to reviewed public evidence. Ref remains
// the author's tracking location; commit and directory bytes are immutable.
type SourceExpectation struct {
	RepositoryID int64  `json:"repository_id"`
	CommitSHA    string `json:"commit_sha"`
	SHA256       string `json:"sha256"`
}

func (e SourceExpectation) Validate() error {
	if e.RepositoryID < 1 || e.RepositoryID > 9007199254740991 || !gitSHAPattern.MatchString(e.CommitSHA) {
		return fail("TEMPLATE_SOURCE_INVALID", "The expected repository or commit identity is invalid.")
	}
	b, err := hex.DecodeString(e.SHA256)
	if err != nil || len(b) != 32 || hex.EncodeToString(b) != e.SHA256 {
		return fail("TEMPLATE_SOURCE_INVALID", "The expected directory digest is invalid.")
	}
	return nil
}
func (e SourceExpectation) Verify(snapshot Snapshot) error {
	if err := e.Validate(); err != nil {
		return err
	}
	if err := snapshot.Validate(); err != nil {
		return err
	}
	if snapshot.Source.RepositoryID != e.RepositoryID || snapshot.Source.CommitSHA != e.CommitSHA || snapshot.SHA256 != e.SHA256 {
		return fail("TEMPLATE_SOURCE_EXPECTATION_MISMATCH", "The source differs from the selected repository, commit, or directory digest.")
	}
	return nil
}

// SourceIdentity preserves Redeven's existing imported template namespace.
func SourceIdentity(repositoryID int64, documentID string) string {
	h := sha256.New()
	for _, p := range []string{strconv.FormatInt(repositoryID, 10), documentID} {
		h.Write([]byte(p))
		h.Write([]byte{0})
	}
	return "git_" + fmt.Sprintf("%x", h.Sum(nil))[:48]
}

func (c *GitHubClient) CapturePinned(ctx context.Context, input GitSource, expected SourceExpectation, token string) (Snapshot, error) {
	if err := expected.Validate(); err != nil {
		return Snapshot{}, err
	}
	source, tail, err := parseGitSource(input)
	if err != nil {
		return Snapshot{}, err
	}
	if len(tail) != 0 || source.Ref == "" {
		return Snapshot{}, fail("TEMPLATE_SOURCE_INVALID", "Pinned acquisition requires an explicit repository, ref, and directory.")
	}
	if token == "" {
		return c.capturePublicArchive(ctx, source, expected)
	}
	trackingRef := source.Ref
	source.Ref = expected.CommitSHA
	resolved, err := c.Resolve(ctx, source, token)
	if err != nil {
		return Snapshot{}, err
	}
	if resolved.RepositoryID != expected.RepositoryID || resolved.CommitSHA != expected.CommitSHA {
		return Snapshot{}, fail("TEMPLATE_SOURCE_EXPECTATION_MISMATCH", "The resolved source differs from the selected identity.")
	}
	snapshot, err := c.CaptureResolved(ctx, resolved, token)
	if err != nil {
		return Snapshot{}, err
	}
	snapshot.Source.Ref = trackingRef
	if err := expected.Verify(snapshot); err != nil {
		return Snapshot{}, err
	}
	return snapshot, nil
}

// MarketTemplate is a passive projection derived only from verified originals.
// It intentionally carries no executable specification or credential.
type MarketTemplate struct {
	TemplateID            string                  `json:"template_id"`
	DocumentTemplateID    string                  `json:"document_template_id"`
	ServiceFamilyID       string                  `json:"service_family_id"`
	Revision              int64                   `json:"revision"`
	Source                ResolvedSource          `json:"source"`
	SHA256                string                  `json:"sha256"`
	SourceDocumentVersion int                     `json:"source_document_version"`
	SourceSpecVersion     int                     `json:"source_spec_version"`
	DefaultLocale         string                  `json:"default_locale"`
	Localizations         map[string]Localization `json:"localizations"`
	Icon                  *IconAsset              `json:"icon,omitempty"`
	Deployment            Deployment              `json:"deployment"`
	SupportedPlatforms    []string                `json:"supported_platforms"`
	RecommendedVersion    string                  `json:"recommended_version,omitempty"`
	DeveloperPreview      bool                    `json:"developer_preview"`
}

func InspectMarketTemplate(snapshot Snapshot) (*MarketTemplate, error) {
	if err := snapshot.Validate(); err != nil {
		return nil, err
	}
	parsed, err := Read(snapshot.Files)
	if err != nil {
		return nil, err
	}
	doc := parsed.Document
	platforms := append([]string{}, doc.SupportedPlatforms...)
	if len(platforms) == 0 {
		for _, p := range []string{"linux-amd64", "linux-arm64", "darwin-amd64", "darwin-arm64"} {
			if _, err := parsed.ForPlatform(p); err == nil {
				platforms = append(platforms, p)
			}
		}
	}
	return &MarketTemplate{TemplateID: SourceIdentity(snapshot.Source.RepositoryID, doc.TemplateID), DocumentTemplateID: doc.TemplateID, ServiceFamilyID: SourceIdentity(snapshot.Source.RepositoryID, doc.ServiceFamilyID), Revision: doc.Revision, Source: snapshot.Source, SHA256: snapshot.SHA256, SourceDocumentVersion: parsed.SourceDocumentVersion, SourceSpecVersion: parsed.SourceSpecVersion, DefaultLocale: doc.DefaultLocale, Localizations: parsed.Localizations, Icon: parsed.Icon, Deployment: parsed.Spec.Kind, SupportedPlatforms: platforms, RecommendedVersion: doc.RecommendedVersion, DeveloperPreview: doc.DeveloperPreview}, nil
}
