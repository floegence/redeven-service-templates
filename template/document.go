package template

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"path"
	"regexp"
	"slices"
	"strings"
)

const (
	Kind                = "redeven.service-template"
	Filename            = "redeven-service-template.json"
	DocumentVersion     = 3
	SpecVersion         = 6
	ArtifactPlaceholder = "${REDEVEN_CATALOG_ARTIFACT}"
)

var exactArtifactPattern = regexp.MustCompile(`^[^\s@]+@sha256:[0-9a-f]{64}$`)
var platformPattern = regexp.MustCompile(`^(linux|darwin)-(amd64|arm64)$`)

type Error struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}

func (e *Error) Error() string                                        { return e.Message }
func fail(code, message string) error                                 { return &Error{Code: code, Message: message} }
func serviceError(code, message string, _ int, _ bool, _ error) error { return fail(code, message) }

type Notice struct {
	ID                      string `json:"id"`
	Revision                int64  `json:"revision"`
	Severity                string `json:"severity"`
	AcknowledgementRequired bool   `json:"acknowledgement_required"`
}
type LocalizedNotice struct {
	Title       string `json:"title"`
	Description string `json:"description"`
}
type Localization struct {
	Name        string                     `json:"name"`
	Description string                     `json:"description"`
	Notices     map[string]LocalizedNotice `json:"notices,omitempty"`
}
type IconReference struct {
	Path      string `json:"path"`
	MediaType string `json:"media_type"`
}
type IconAsset struct {
	MediaType string `json:"media_type"`
	Data      string `json:"data"`
	SHA256    string `json:"sha256"`
}
type ReleaseDiscovery struct {
	Source string `json:"source"`
}

type Document struct {
	Kind               string            `json:"kind"`
	SchemaVersion      int               `json:"schema_version"`
	TemplateID         string            `json:"template_id"`
	ServiceFamilyID    string            `json:"service_family_id"`
	Revision           int64             `json:"revision"`
	DefaultLocale      string            `json:"default_locale"`
	Locales            []string          `json:"locales"`
	RecommendedVersion string            `json:"recommended_version,omitempty"`
	SortOrder          int               `json:"sort_order,omitempty"`
	DeveloperPreview   bool              `json:"developer_preview,omitempty"`
	DiskBytes          int64             `json:"disk_bytes,omitempty"`
	SourceURL          string            `json:"source_url,omitempty"`
	DockerSourceURL    string            `json:"docker_source_url,omitempty"`
	Deployment         Deployment        `json:"deployment,omitempty"`
	ContainerMode      string            `json:"container_mode,omitempty"`
	DefaultAccessMode  string            `json:"default_access_mode,omitempty"`
	SupportedPlatforms []string          `json:"supported_platforms,omitempty"`
	PlatformArtifacts  map[string]string `json:"platform_artifacts,omitempty"`
	ReleaseDiscovery   *ReleaseDiscovery `json:"release_discovery,omitempty"`
	Notices            []Notice          `json:"notices,omitempty"`
	Icon               *IconReference    `json:"icon,omitempty"`
	Spec               json.RawMessage   `json:"spec"`
}

type Template struct {
	Document              Document                `json:"document"`
	SourceDocumentVersion int                     `json:"source_document_version"`
	SourceSpecVersion     int                     `json:"source_spec_version"`
	Spec                  Spec                    `json:"spec"`
	Localizations         map[string]Localization `json:"localizations"`
	Icon                  *IconAsset              `json:"icon,omitempty"`
	SHA256                string                  `json:"sha256"`
}

func DecodeJSON(raw []byte, target any) error {
	// A second structural pass rejects duplicate keys, including nested keys.
	d := json.NewDecoder(bytes.NewReader(raw))
	if err := uniqueJSONValue(d); err != nil {
		return err
	}
	if _, err := d.Token(); !errors.Is(err, io.EOF) {
		return errors.New("trailing JSON content")
	}
	d = json.NewDecoder(bytes.NewReader(raw))
	d.DisallowUnknownFields()
	return d.Decode(target)
}
func uniqueJSONValue(d *json.Decoder) error {
	token, err := d.Token()
	if err != nil {
		return err
	}
	delim, ok := token.(json.Delim)
	if !ok {
		return nil
	}
	switch delim {
	case '{':
		keys := map[string]bool{}
		for d.More() {
			key, err := d.Token()
			if err != nil {
				return err
			}
			s, ok := key.(string)
			if !ok || keys[s] {
				return errors.New("duplicate or invalid JSON key")
			}
			keys[s] = true
			if err := uniqueJSONValue(d); err != nil {
				return err
			}
		}
	case '[':
		for d.More() {
			if err := uniqueJSONValue(d); err != nil {
				return err
			}
		}
	default:
		return errors.New("invalid JSON structure")
	}
	_, err = d.Token()
	return err
}

func Read(files []File) (*Template, error) {
	digest, err := Digest(files)
	if err != nil {
		return nil, err
	}
	byPath := map[string][]byte{}
	for _, file := range files {
		byPath[file.Path] = file.Content
	}
	current, hasCurrent := byPath[Filename]
	legacy, hasLegacy := byPath["template.json"]
	if hasCurrent && hasLegacy {
		return nil, fail("TEMPLATE_ENTRYPOINT_CONFLICT", "The template directory contains both current and historical entrypoints.")
	}
	if !hasCurrent && !hasLegacy {
		return nil, fail("TEMPLATE_ENTRYPOINT_MISSING", "The selected directory has no Redeven Service Template declaration.")
	}
	raw := current
	if hasLegacy {
		raw = legacy
	}
	doc, version, err := readDocument(raw, hasLegacy, files)
	if err != nil {
		return nil, err
	}
	spec, sourceSpec, err := NormalizeSpec(doc.Spec)
	if err != nil {
		return nil, err
	}
	if doc.Deployment != "" && doc.Deployment != spec.Kind {
		return nil, fail("TEMPLATE_DEPLOYMENT_INVALID", "The document and execution deployment types differ.")
	}
	doc.Deployment = spec.Kind
	doc.Spec, _ = json.Marshal(spec)
	result := &Template{Document: doc, SourceDocumentVersion: version, SourceSpecVersion: sourceSpec, Spec: spec, Localizations: map[string]Localization{}, SHA256: digest}
	if err := validateDocument(doc); err != nil {
		return nil, err
	}
	for _, locale := range doc.Locales {
		var localized Localization
		if err := DecodeJSON(byPath["locales/"+locale+".json"], &localized); err != nil {
			return nil, fail("TEMPLATE_LOCALIZATION_INVALID", fmt.Sprintf("The %s localization is missing or invalid.", locale))
		}
		if strings.TrimSpace(localized.Name) == "" || len(localized.Name) > 320 || strings.TrimSpace(localized.Description) == "" || len(localized.Description) > 4000 || len(localized.Notices) != len(doc.Notices) {
			return nil, fail("TEMPLATE_LOCALIZATION_INVALID", fmt.Sprintf("The %s localization is incomplete.", locale))
		}
		for _, notice := range doc.Notices {
			copy, ok := localized.Notices[notice.ID]
			if !ok || strings.TrimSpace(copy.Title) == "" || strings.TrimSpace(copy.Description) == "" {
				return nil, fail("TEMPLATE_LOCALIZATION_INVALID", "A localized notice is missing.")
			}
		}
		result.Localizations[locale] = localized
	}
	if doc.Icon != nil {
		data := byPath[doc.Icon.Path]
		if err := ValidateSVG(*doc.Icon, data); err != nil {
			return nil, fail("TEMPLATE_ICON_INVALID", "The template icon is not a safe SVG image.")
		}
		result.Icon = &IconAsset{MediaType: doc.Icon.MediaType, Data: string(data), SHA256: hashBytes(data)}
	}
	return result, nil
}

func validateDocument(doc Document) error {
	if doc.Kind != Kind || doc.SchemaVersion != DocumentVersion {
		return fail("TEMPLATE_IDENTITY_INVALID", "The declaration is not a supported Redeven Service Template.")
	}
	if !managedWorkspaceIdentityPattern.MatchString(doc.TemplateID) || !managedWorkspaceIdentityPattern.MatchString(doc.ServiceFamilyID) || doc.Revision < 1 || doc.DiskBytes < 0 {
		return fail("TEMPLATE_IDENTITY_INVALID", "Template identity or revision is invalid.")
	}
	if len(doc.Locales) == 0 || len(doc.Locales) > 100 || !slices.Contains(doc.Locales, doc.DefaultLocale) {
		return fail("TEMPLATE_LOCALIZATION_INVALID", "The default locale must be one of the explicitly supported locales.")
	}
	seen := map[string]bool{}
	for _, locale := range doc.Locales {
		key := strings.ToLower(locale)
		if !regexp.MustCompile(`^[a-zA-Z]{2,8}(?:-[a-zA-Z0-9]{1,8})*$`).MatchString(locale) || seen[key] {
			return fail("TEMPLATE_LOCALIZATION_INVALID", "Template locales must be distinct language tags.")
		}
		seen[key] = true
	}
	if doc.DefaultAccessMode != "" && doc.DefaultAccessMode != "unified_proxy" && doc.DefaultAccessMode != "desktop_loopback" {
		return fail("TEMPLATE_ACCESS_MODE_INVALID", "Template access mode is unsupported.")
	}
	seenPlatforms := map[string]bool{}
	for _, platform := range doc.SupportedPlatforms {
		if !platformPattern.MatchString(platform) || seenPlatforms[platform] {
			return fail("TEMPLATE_PLATFORM_INVALID", "Supported platforms must be distinct OS/architecture pairs.")
		}
		seenPlatforms[platform] = true
	}
	for platform, artifact := range doc.PlatformArtifacts {
		if !platformPattern.MatchString(platform) || (len(doc.SupportedPlatforms) > 0 && !seenPlatforms[platform]) || !exactArtifactPattern.MatchString(artifact) {
			return fail("TEMPLATE_ARTIFACT_INVALID", "Platform artifacts require a declared platform and exact image digest.")
		}
	}
	if doc.Icon != nil && (!validPath(doc.Icon.Path) || path.Ext(doc.Icon.Path) != ".svg") {
		return fail("TEMPLATE_ICON_INVALID", "The icon must reference a local SVG file.")
	}
	for _, raw := range []string{doc.SourceURL, doc.DockerSourceURL} {
		if raw != "" && !publicHTTPS(raw) {
			return fail("TEMPLATE_SOURCE_INVALID", "Source links must use public HTTPS URLs without credentials.")
		}
	}
	seen = map[string]bool{}
	for _, notice := range doc.Notices {
		if !managedWorkspaceIdentityPattern.MatchString(notice.ID) || notice.Revision < 1 || (notice.Severity != "info" && notice.Severity != "warning") || seen[notice.ID] {
			return fail("TEMPLATE_NOTICE_INVALID", "Template notices require distinct identities, revisions, and supported severity.")
		}
		seen[notice.ID] = true
	}
	return nil
}

func (t *Template) Localized(locale string) Localization {
	for key, value := range t.Localizations {
		if strings.EqualFold(key, locale) {
			return value
		}
	}
	if base, _, ok := strings.Cut(locale, "-"); ok {
		for key, value := range t.Localizations {
			if strings.EqualFold(key, base) {
				return value
			}
		}
	}
	return t.Localizations[t.Document.DefaultLocale]
}
