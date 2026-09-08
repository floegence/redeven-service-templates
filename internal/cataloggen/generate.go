package cataloggen

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"encoding/xml"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

const CatalogVersion = "v0.4.3"

var Locales = []string{
	"en-US", "zh-CN", "zh-TW", "ja-JP", "ko-KR",
	"de-DE", "fr-FR", "es-ES", "pt-BR", "ru-RU",
}

type Notice struct {
	ID                      string `json:"id"`
	Revision                int    `json:"revision"`
	Severity                string `json:"severity"`
	AcknowledgementRequired bool   `json:"acknowledgement_required"`
}

type IconReference struct {
	Path      string `json:"path"`
	MediaType string `json:"media_type"`
}

type ReleaseDiscovery struct {
	Source string `json:"source"`
}

type TemplateDefinition struct {
	SchemaVersion      int               `json:"schema_version"`
	TemplateID         string            `json:"template_id"`
	ServiceFamilyID    string            `json:"service_family_id"`
	RecommendedVersion string            `json:"recommended_version"`
	Revision           int               `json:"revision"`
	SortOrder          int               `json:"sort_order"`
	DeveloperPreview   bool              `json:"developer_preview"`
	DiskBytes          int64             `json:"disk_bytes"`
	SourceURL          string            `json:"source_url"`
	DockerSourceURL    string            `json:"docker_source_url,omitempty"`
	Deployment         string            `json:"deployment"`
	ContainerMode      string            `json:"container_mode,omitempty"`
	DefaultAccessMode  string            `json:"default_access_mode"`
	SupportedPlatforms []string          `json:"supported_platforms,omitempty"`
	PlatformArtifacts  map[string]string `json:"platform_artifacts,omitempty"`
	ReleaseDiscovery   *ReleaseDiscovery `json:"release_discovery,omitempty"`
	Notices            []Notice          `json:"notices"`
	Icon               IconReference     `json:"icon"`
	Spec               json.RawMessage   `json:"spec"`
}

type LocalizedNotice struct {
	Title       string `json:"title"`
	Description string `json:"description"`
}

type Localization struct {
	Name        string                     `json:"name"`
	Description string                     `json:"description"`
	Notices     map[string]LocalizedNotice `json:"notices"`
}

type IconAsset struct {
	MediaType string `json:"media_type"`
	Data      string `json:"data"`
	SHA256    string `json:"sha256"`
}

type BundleTemplate struct {
	SchemaVersion      int                     `json:"schema_version"`
	TemplateID         string                  `json:"template_id"`
	ServiceFamilyID    string                  `json:"service_family_id"`
	RecommendedVersion string                  `json:"recommended_version"`
	Revision           int                     `json:"revision"`
	SortOrder          int                     `json:"sort_order"`
	DeveloperPreview   bool                    `json:"developer_preview"`
	DiskBytes          int64                   `json:"disk_bytes"`
	SourceURL          string                  `json:"source_url"`
	DockerSourceURL    string                  `json:"docker_source_url,omitempty"`
	Deployment         string                  `json:"deployment"`
	ContainerMode      string                  `json:"container_mode,omitempty"`
	DefaultAccessMode  string                  `json:"default_access_mode"`
	SupportedPlatforms []string                `json:"supported_platforms,omitempty"`
	PlatformArtifacts  map[string]string       `json:"platform_artifacts,omitempty"`
	ReleaseDiscovery   *ReleaseDiscovery       `json:"release_discovery,omitempty"`
	Notices            []Notice                `json:"notices"`
	Spec               json.RawMessage         `json:"spec"`
	Localizations      map[string]Localization `json:"localizations"`
	Icon               IconAsset               `json:"icon"`
}

type Bundle struct {
	SchemaVersion  int              `json:"schema_version"`
	CatalogVersion string           `json:"catalog_version"`
	Locales        []string         `json:"locales"`
	Templates      []BundleTemplate `json:"templates"`
}

type ManifestInput struct {
	Path   string `json:"path"`
	SHA256 string `json:"sha256"`
}

type Manifest struct {
	SchemaVersion  int             `json:"schema_version"`
	CatalogVersion string          `json:"catalog_version"`
	BundlePath     string          `json:"bundle_path"`
	BundleSHA256   string          `json:"bundle_sha256"`
	Inputs         []ManifestInput `json:"inputs"`
}

type Output struct {
	Bundle   []byte
	Manifest []byte
	SHA256   []byte
}

func Generate(root string) (Output, error) {
	return GenerateWithVersion(root, CatalogVersion)
}

// GenerateWithVersion builds a deterministic bundle using the supplied
// catalog version. The explicit version lets release tooling stage a new
// catalog atomically before changing this package's source constant.
func GenerateWithVersion(root, catalogVersion string) (Output, error) {
	if strings.TrimSpace(catalogVersion) == "" {
		return Output{}, errors.New("catalog version is required")
	}
	if _, err := os.Stat(filepath.Join(root, "compatibility")); err == nil {
		return Output{}, errors.New("compatibility directory is forbidden")
	} else if !errors.Is(err, os.ErrNotExist) {
		return Output{}, fmt.Errorf("inspect compatibility directory: %w", err)
	}

	templatesRoot := filepath.Join(root, "templates")
	entries, err := os.ReadDir(templatesRoot)
	if err != nil {
		return Output{}, fmt.Errorf("read templates: %w", err)
	}
	sort.Slice(entries, func(i, j int) bool { return entries[i].Name() < entries[j].Name() })

	seenIDs := make(map[string]struct{}, len(entries))
	bundleTemplates := make([]BundleTemplate, 0, len(entries))
	inputs := make([]ManifestInput, 0, len(entries)*(len(Locales)+2))
	for _, entry := range entries {
		if !entry.IsDir() {
			return Output{}, fmt.Errorf("unexpected file in templates directory: %s", entry.Name())
		}
		dir := filepath.Join(templatesRoot, entry.Name())
		definitionPath := filepath.Join(dir, "template.json")
		var definition TemplateDefinition
		definitionBytes, err := readStrictJSON(definitionPath, &definition)
		if err != nil {
			return Output{}, err
		}
		if err := validateDefinition(entry.Name(), definition); err != nil {
			return Output{}, fmt.Errorf("validate %s: %w", definitionPath, err)
		}
		if _, duplicate := seenIDs[definition.TemplateID]; duplicate {
			return Output{}, fmt.Errorf("duplicate template id %q", definition.TemplateID)
		}
		seenIDs[definition.TemplateID] = struct{}{}
		inputs = append(inputs, inputHash(root, definitionPath, definitionBytes))

		localizations := make(map[string]Localization, len(Locales))
		for _, locale := range Locales {
			localizationPath := filepath.Join(dir, "locales", locale+".json")
			var localization Localization
			localizationBytes, err := readStrictJSON(localizationPath, &localization)
			if err != nil {
				return Output{}, err
			}
			if err := validateLocalization(definition.Notices, localization); err != nil {
				return Output{}, fmt.Errorf("validate %s: %w", localizationPath, err)
			}
			localizations[locale] = localization
			inputs = append(inputs, inputHash(root, localizationPath, localizationBytes))
		}
		if err := rejectUnexpectedLocales(filepath.Join(dir, "locales")); err != nil {
			return Output{}, err
		}

		iconPath := filepath.Join(dir, filepath.FromSlash(definition.Icon.Path))
		iconBytes, err := os.ReadFile(iconPath)
		if err != nil {
			return Output{}, fmt.Errorf("read icon %s: %w", iconPath, err)
		}
		if err := validateSVG(definition.Icon, iconBytes); err != nil {
			return Output{}, fmt.Errorf("validate icon %s: %w", iconPath, err)
		}
		inputs = append(inputs, inputHash(root, iconPath, iconBytes))

		bundleTemplates = append(bundleTemplates, BundleTemplate{
			SchemaVersion: definition.SchemaVersion, TemplateID: definition.TemplateID,
			ServiceFamilyID: definition.ServiceFamilyID, RecommendedVersion: definition.RecommendedVersion,
			Revision: definition.Revision, SortOrder: definition.SortOrder,
			DeveloperPreview: definition.DeveloperPreview, DiskBytes: definition.DiskBytes,
			SourceURL: definition.SourceURL, DockerSourceURL: definition.DockerSourceURL,
			Deployment: definition.Deployment, ContainerMode: definition.ContainerMode,
			DefaultAccessMode:  definition.DefaultAccessMode,
			SupportedPlatforms: append([]string(nil), definition.SupportedPlatforms...),
			PlatformArtifacts:  definition.PlatformArtifacts, ReleaseDiscovery: definition.ReleaseDiscovery,
			Notices: append([]Notice(nil), definition.Notices...), Spec: definition.Spec,
			Localizations: localizations,
			Icon:          IconAsset{MediaType: definition.Icon.MediaType, Data: string(iconBytes), SHA256: digest(iconBytes)},
		})
	}
	if len(bundleTemplates) == 0 {
		return Output{}, errors.New("catalog must contain at least one template")
	}
	sort.Slice(bundleTemplates, func(i, j int) bool {
		if bundleTemplates[i].SortOrder == bundleTemplates[j].SortOrder {
			return bundleTemplates[i].TemplateID < bundleTemplates[j].TemplateID
		}
		return bundleTemplates[i].SortOrder < bundleTemplates[j].SortOrder
	})
	sort.Slice(inputs, func(i, j int) bool { return inputs[i].Path < inputs[j].Path })

	bundleBytes, err := marshal(Bundle{SchemaVersion: 2, CatalogVersion: catalogVersion, Locales: append([]string(nil), Locales...), Templates: bundleTemplates})
	if err != nil {
		return Output{}, fmt.Errorf("encode bundle: %w", err)
	}
	bundleDigest := digest(bundleBytes)
	manifestBytes, err := marshal(Manifest{SchemaVersion: 1, CatalogVersion: catalogVersion, BundlePath: "dist/catalog.bundle.json", BundleSHA256: bundleDigest, Inputs: inputs})
	if err != nil {
		return Output{}, fmt.Errorf("encode manifest: %w", err)
	}
	return Output{Bundle: bundleBytes, Manifest: manifestBytes, SHA256: []byte(bundleDigest + "\n")}, nil
}

func Write(root string, output Output) error {
	dist := filepath.Join(root, "dist")
	if err := os.MkdirAll(dist, 0o755); err != nil {
		return err
	}
	for path, data := range map[string][]byte{
		filepath.Join(dist, "catalog.bundle.json"):          output.Bundle,
		filepath.Join(dist, "catalog.bundle.manifest.json"): output.Manifest,
		filepath.Join(dist, "catalog.bundle.sha256"):        output.SHA256,
	} {
		if err := os.WriteFile(path, data, 0o644); err != nil {
			return fmt.Errorf("write %s: %w", path, err)
		}
	}
	return nil
}

func Verify(root string, output Output) error {
	for path, want := range map[string][]byte{
		filepath.Join(root, "dist", "catalog.bundle.json"):          output.Bundle,
		filepath.Join(root, "dist", "catalog.bundle.manifest.json"): output.Manifest,
		filepath.Join(root, "dist", "catalog.bundle.sha256"):        output.SHA256,
	} {
		got, err := os.ReadFile(path)
		if err != nil {
			return fmt.Errorf("read generated file %s: %w", path, err)
		}
		if !bytes.Equal(got, want) {
			return fmt.Errorf("generated file is stale: %s", path)
		}
	}
	return nil
}

func validateDefinition(directory string, definition TemplateDefinition) error {
	if definition.SchemaVersion != 2 || definition.TemplateID != directory || definition.ServiceFamilyID == "" || definition.RecommendedVersion == "" || definition.Revision < 1 || definition.DiskBytes < 1 {
		return errors.New("identity, recommended version, revision, or disk requirement is invalid")
	}
	if !validHTTPS(definition.SourceURL) || (definition.DockerSourceURL != "" && !validHTTPS(definition.DockerSourceURL)) {
		return errors.New("source URLs must use HTTPS")
	}
	if definition.Icon.Path != "assets/icon.svg" || definition.Icon.MediaType != "image/svg+xml" {
		return errors.New("icon must be the reviewed local SVG asset")
	}
	if definition.Deployment != "host" && definition.Deployment != "container" && definition.Deployment != "compose" {
		return fmt.Errorf("unsupported deployment %q", definition.Deployment)
	}
	var specHeader struct {
		SchemaVersion int    `json:"schema_version"`
		Kind          string `json:"kind"`
	}
	if err := json.Unmarshal(definition.Spec, &specHeader); err != nil || specHeader.SchemaVersion != 5 || specHeader.Kind != definition.Deployment {
		return errors.New("TemplateSpec v5 kind must match deployment")
	}
	var retiredPolicy struct {
		Container *struct {
			ReleasePolicy json.RawMessage `json:"release_policy"`
		} `json:"container"`
	}
	if err := json.Unmarshal(definition.Spec, &retiredPolicy); err != nil {
		return errors.New("TemplateSpec v5 is invalid")
	}
	if retiredPolicy.Container != nil && len(retiredPolicy.Container.ReleasePolicy) != 0 {
		return errors.New("TemplateSpec v5 cannot restrict source release selection")
	}
	var openTargetSpec struct {
		Host *struct {
			OpenTarget *struct {
				Mode       string `json:"mode"`
				LinePrefix string `json:"line_prefix"`
			} `json:"open_target"`
		} `json:"host"`
		Container *struct {
			OpenTarget json.RawMessage `json:"open_target"`
		} `json:"container"`
		Compose *struct {
			OpenTarget json.RawMessage `json:"open_target"`
		} `json:"compose"`
	}
	if err := json.Unmarshal(definition.Spec, &openTargetSpec); err != nil {
		return errors.New("TemplateSpec v5 is invalid")
	}
	if openTargetSpec.Container != nil && len(openTargetSpec.Container.OpenTarget) != 0 || openTargetSpec.Compose != nil && len(openTargetSpec.Compose.OpenTarget) != 0 {
		return errors.New("open target is supported only for host templates")
	}
	if openTargetSpec.Host != nil && openTargetSpec.Host.OpenTarget != nil {
		openTarget := openTargetSpec.Host.OpenTarget
		if openTarget.Mode != "startup_output_url" || !validLinePrefix(openTarget.LinePrefix) {
			return errors.New("host open target is invalid")
		}
	}
	if definition.Deployment == "host" {
		if definition.ReleaseDiscovery == nil || definition.ReleaseDiscovery.Source != "npm" || len(definition.SupportedPlatforms) == 0 || len(definition.PlatformArtifacts) != 0 {
			return errors.New("host template must declare npm discovery and supported platforms")
		}
		var spec struct {
			Host *struct {
				NPM *struct {
					Version string `json:"version"`
				} `json:"npm"`
			} `json:"host"`
		}
		if err := json.Unmarshal(definition.Spec, &spec); err != nil || spec.Host == nil || spec.Host.NPM == nil || spec.Host.NPM.Version != definition.RecommendedVersion {
			return errors.New("host recommended version must equal the exact npm package version")
		}
	} else if definition.Deployment == "container" {
		if definition.ContainerMode != "single" || definition.ReleaseDiscovery == nil || definition.ReleaseDiscovery.Source != "oci" || len(definition.PlatformArtifacts) == 0 {
			return errors.New("container template must declare single-container OCI artifacts")
		}
		for platform, artifact := range definition.PlatformArtifacts {
			parts := strings.Split(artifact, "@sha256:")
			if !strings.HasPrefix(platform, "linux-") || len(parts) != 2 || len(parts[1]) != 64 {
				return fmt.Errorf("platform artifact %q must use an exact sha256 digest", platform)
			}
			if _, err := hex.DecodeString(parts[1]); err != nil {
				return fmt.Errorf("platform artifact %q has invalid digest", platform)
			}
			if tag := imageTag(parts[0]); tag != definition.RecommendedVersion {
				return fmt.Errorf("platform artifact %q tag %q does not match recommended version %q", platform, tag, definition.RecommendedVersion)
			}
		}
		if !bytes.Contains(definition.Spec, []byte("${REDEVEN_CATALOG_ARTIFACT}")) {
			return errors.New("container spec must use the catalog artifact placeholder")
		}
	} else if definition.ReleaseDiscovery != nil {
		return errors.New("Compose templates cannot declare single-release discovery")
	}
	noticeIDs := make(map[string]struct{}, len(definition.Notices))
	for _, notice := range definition.Notices {
		if notice.ID == "" || notice.Revision < 1 || (notice.Severity != "info" && notice.Severity != "warning") {
			return errors.New("notice is invalid")
		}
		if _, duplicate := noticeIDs[notice.ID]; duplicate {
			return fmt.Errorf("duplicate notice %q", notice.ID)
		}
		noticeIDs[notice.ID] = struct{}{}
	}
	return nil
}

func validLinePrefix(value string) bool {
	if strings.TrimSpace(value) == "" || len(value) > 128 {
		return false
	}
	for _, r := range value {
		if r < 0x20 || r == 0x7f {
			return false
		}
	}
	return true
}

func imageTag(reference string) string {
	lastSlash, lastColon := strings.LastIndex(reference, "/"), strings.LastIndex(reference, ":")
	if lastColon <= lastSlash {
		return ""
	}
	return strings.TrimSpace(reference[lastColon+1:])
}

func validateLocalization(notices []Notice, localization Localization) error {
	if strings.TrimSpace(localization.Name) == "" || strings.TrimSpace(localization.Description) == "" {
		return errors.New("name and description are required")
	}
	if len(localization.Notices) != len(notices) {
		return errors.New("localized notice set does not match template notices")
	}
	for _, notice := range notices {
		localized, ok := localization.Notices[notice.ID]
		if !ok || strings.TrimSpace(localized.Title) == "" || strings.TrimSpace(localized.Description) == "" {
			return fmt.Errorf("notice %q is missing or empty", notice.ID)
		}
	}
	return nil
}

func rejectUnexpectedLocales(dir string) error {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return err
	}
	want := make(map[string]struct{}, len(Locales))
	for _, locale := range Locales {
		want[locale+".json"] = struct{}{}
	}
	for _, entry := range entries {
		if entry.IsDir() {
			return fmt.Errorf("unexpected locale directory %s", filepath.Join(dir, entry.Name()))
		}
		if _, ok := want[entry.Name()]; !ok {
			return fmt.Errorf("unexpected locale file %s", filepath.Join(dir, entry.Name()))
		}
	}
	if len(entries) != len(want) {
		return fmt.Errorf("locale directory %s is incomplete", dir)
	}
	return nil
}

func validateSVG(reference IconReference, data []byte) error {
	if len(data) == 0 || len(data) > 64*1024 || reference.MediaType != "image/svg+xml" {
		return errors.New("SVG size or media type is invalid")
	}
	decoder := xml.NewDecoder(bytes.NewReader(data))
	rootSeen := false
	for {
		token, err := decoder.Token()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return fmt.Errorf("parse SVG: %w", err)
		}
		element, ok := token.(xml.StartElement)
		if !ok {
			continue
		}
		if !rootSeen {
			if element.Name.Local != "svg" {
				return errors.New("asset root must be svg")
			}
			rootSeen = true
		}
		if element.Name.Local == "script" || element.Name.Local == "foreignObject" {
			return fmt.Errorf("unsafe SVG element %q", element.Name.Local)
		}
		for _, attribute := range element.Attr {
			name := strings.ToLower(attribute.Name.Local)
			value := strings.ToLower(strings.TrimSpace(attribute.Value))
			if strings.HasPrefix(name, "on") || name == "href" || strings.HasPrefix(value, "javascript:") || strings.HasPrefix(value, "data:") {
				return fmt.Errorf("unsafe SVG attribute %q", attribute.Name.Local)
			}
		}
	}
	if !rootSeen {
		return errors.New("SVG root is missing")
	}
	return nil
}

func readStrictJSON(path string, target any) ([]byte, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read %s: %w", path, err)
	}
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil {
		return nil, fmt.Errorf("decode %s: %w", path, err)
	}
	if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		return nil, fmt.Errorf("decode %s: trailing JSON content", path)
	}
	return data, nil
}

func inputHash(root, path string, data []byte) ManifestInput {
	relative, _ := filepath.Rel(root, path)
	return ManifestInput{Path: filepath.ToSlash(relative), SHA256: digest(data)}
}

func validHTTPS(rawURL string) bool {
	parsed, err := url.Parse(rawURL)
	return err == nil && parsed.Scheme == "https" && parsed.Host != "" && parsed.User == nil && parsed.RawQuery == "" && parsed.Fragment == ""
}

func digest(data []byte) string {
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}

func marshal(value any) ([]byte, error) {
	data, err := json.Marshal(value)
	if err != nil {
		return nil, err
	}
	return append(data, '\n'), nil
}

func HasForbiddenCompatibilityCode(root string) error {
	return filepath.WalkDir(root, func(path string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		relative, _ := filepath.Rel(root, path)
		if entry.IsDir() && entry.Name() == "compatibility" {
			return fmt.Errorf("forbidden compatibility directory: %s", relative)
		}
		if !entry.IsDir() && strings.HasPrefix(filepath.ToSlash(relative), "templates/") {
			info, err := entry.Info()
			if err != nil {
				return err
			}
			if info.Mode()&0o111 != 0 {
				return fmt.Errorf("template input must not be executable: %s", relative)
			}
		}
		return nil
	})
}
