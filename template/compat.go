package template

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/url"
	"slices"
	"strings"
)

func hashBytes(raw []byte) string { sum := sha256.Sum256(raw); return hex.EncodeToString(sum[:]) }
func publicHTTPS(raw string) bool {
	u, err := url.Parse(raw)
	return err == nil && u.Scheme == "https" && u.Hostname() != "" && u.User == nil && u.Fragment == ""
}

func readDocument(raw []byte, legacy bool, files []File) (Document, int, error) {
	var fields map[string]json.RawMessage
	if err := DecodeJSON(raw, &fields); err != nil {
		return Document{}, 0, fail("TEMPLATE_DOCUMENT_INVALID", "The template declaration is not strict JSON.")
	}
	var version int
	if err := json.Unmarshal(fields["schema_version"], &version); err != nil {
		return Document{}, 0, fail("TEMPLATE_SCHEMA_UNSUPPORTED", "The template declaration has no supported schema version.")
	}
	original := version
	if (!legacy && version != DocumentVersion) || (legacy && (version < 1 || version > 2)) {
		return Document{}, 0, fail("TEMPLATE_SCHEMA_UNSUPPORTED", "This Redeven Service Template version requires a compatible Redeven release.")
	}
	if legacy {
		for _, key := range []string{"kind", "default_locale", "locales"} {
			if _, ok := fields[key]; ok {
				return Document{}, 0, fail("TEMPLATE_DOCUMENT_INVALID", "The historical template contains fields outside its published schema.")
			}
		}
		// Published v1 -> v2 removed source-version filters. The service release
		// remains exact, while its default became an explicit recommendation.
		if version == 1 {
			if _, ok := fields["recommended_version"]; ok {
				return Document{}, 0, fail("TEMPLATE_DOCUMENT_INVALID", "The v1 document contains v2 recommendation fields.")
			}
			var recommended string
			if err := json.Unmarshal(fields["version"], &recommended); err != nil || recommended == "" {
				return Document{}, 0, fail("TEMPLATE_DOCUMENT_INVALID", "The historical default version is invalid.")
			}
			fields["recommended_version"] = fields["version"]
			delete(fields, "version")
			if rawDiscovery, ok := fields["release_discovery"]; ok && string(rawDiscovery) != "null" {
				var discovery struct {
					Source          string `json:"source"`
					AllowPrerelease bool   `json:"allow_prerelease"`
					AllowNonSemver  bool   `json:"allow_non_semver"`
				}
				if err := DecodeJSON(rawDiscovery, &discovery); err != nil {
					return Document{}, 0, fail("TEMPLATE_DOCUMENT_INVALID", "The historical release discovery declaration is invalid.")
				}
				fields["release_discovery"], _ = json.Marshal(ReleaseDiscovery{Source: discovery.Source})
			}
			version = 2
		}
		if version == 2 {
			locales := []string{}
			for _, file := range files {
				if strings.HasPrefix(file.Path, "locales/") && strings.HasSuffix(file.Path, ".json") && !strings.Contains(strings.TrimPrefix(file.Path, "locales/"), "/") {
					locales = append(locales, strings.TrimSuffix(strings.TrimPrefix(file.Path, "locales/"), ".json"))
				}
			}
			slices.Sort(locales)
			fields["kind"], _ = json.Marshal(Kind)
			fields["default_locale"] = json.RawMessage(`"en-US"`)
			fields["locales"], _ = json.Marshal(locales)
			fields["schema_version"] = json.RawMessage("3")
		}
		raw, _ = json.Marshal(fields)
	}
	var doc Document
	if err := DecodeJSON(raw, &doc); err != nil {
		return Document{}, 0, fail("TEMPLATE_DOCUMENT_INVALID", "The template declaration contains an invalid or unsupported field.")
	}
	var execution struct {
		SchemaVersion int `json:"schema_version"`
	}
	if err := json.Unmarshal(doc.Spec, &execution); err != nil || (original == 1 && execution.SchemaVersion != 3) || (original == 2 && (execution.SchemaVersion < 4 || execution.SchemaVersion > 6)) || (original == 3 && execution.SchemaVersion != 6) {
		return Document{}, 0, fail("TEMPLATE_SCHEMA_UNSUPPORTED", "The document and execution versions do not belong to a published compatibility pair.")
	}
	return doc, original, nil
}

// NormalizeSpec is a pure, versioned data adaptation boundary. Historical
// decoders are never execution drivers and never mutate caller-owned bytes.
func NormalizeSpec(raw []byte) (Spec, int, error) {
	var fields map[string]json.RawMessage
	if err := DecodeJSON(raw, &fields); err != nil {
		return Spec{}, 0, fail("TEMPLATE_SPEC_INVALID", "The execution specification is not strict JSON.")
	}
	var version int
	if err := json.Unmarshal(fields["schema_version"], &version); err != nil || version < 3 || version > SpecVersion {
		return Spec{}, 0, fail("TEMPLATE_SCHEMA_UNSUPPORTED", "The template execution version requires a compatible Redeven release.")
	}
	original := version
	if version < 6 {
		var hostFields map[string]json.RawMessage
		if host, ok := fields["host"]; ok {
			if err := DecodeJSON(host, &hostFields); err != nil {
				return Spec{}, 0, fail("TEMPLATE_SPEC_INVALID", "The historical host declaration is invalid.")
			}
		}
		for _, key := range []string{"after_start_script", "open_script", "output_mode"} {
			if _, ok := hostFields[key]; ok {
				return Spec{}, 0, fail("TEMPLATE_SPEC_INVALID", "The historical host declaration contains a newer field.")
			}
		}
		if version < 5 {
			if _, ok := hostFields["open_target"]; ok {
				return Spec{}, 0, fail("TEMPLATE_SPEC_INVALID", "The historical host declaration contains a newer opening contract.")
			}
		}
		if version == 3 {
			var container map[string]json.RawMessage
			if value, ok := fields["container"]; ok && string(value) != "null" {
				if err := DecodeJSON(value, &container); err != nil {
					return Spec{}, 0, fail("TEMPLATE_SPEC_INVALID", "The historical container specification is invalid.")
				}
				if policy, ok := container["release_policy"]; ok {
					var old struct {
						BlockedTagPrefixes []string `json:"blocked_tag_prefixes"`
					}
					if err := DecodeJSON(policy, &old); err != nil {
						return Spec{}, 0, fail("TEMPLATE_SPEC_INVALID", "The historical release policy is invalid.")
					}
					// The published v4 contract removed tag filtering. Do not revive it.
					delete(container, "release_policy")
					fields["container"], _ = json.Marshal(container)
				}
			}
			fields["schema_version"] = json.RawMessage("4")
			version = 4
		}
		if version == 4 {
			fields["schema_version"] = json.RawMessage("5")
			version = 5
		}
		if version == 5 {
			encoded, _ := json.Marshal(fields)
			next, err := upgradeSpecV5(encoded)
			if err != nil {
				return Spec{}, 0, fail("TEMPLATE_SPEC_INVALID", "The historical execution specification is invalid.")
			}
			raw = next
		}
	}
	var spec Spec
	if err := DecodeJSON(raw, &spec); err != nil {
		return Spec{}, 0, fail("TEMPLATE_SPEC_INVALID", "The execution specification contains an invalid or unknown field.")
	}
	if (spec.Kind == DeploymentHost && (spec.Container != nil || spec.Compose != nil)) || (spec.Kind == DeploymentContainer && (spec.Host != nil || spec.Compose != nil)) || (spec.Kind == DeploymentCompose && (spec.Host != nil || spec.Container != nil)) {
		return Spec{}, 0, fail("TEMPLATE_SPEC_INVALID", "A template must declare exactly one deployment definition.")
	}
	if err := ValidateSpec(spec); err != nil {
		return Spec{}, 0, err
	}
	return spec, original, nil
}

// ForPlatform materializes only the declared platform artifact, leaving the
// original document and its scripts unchanged.
func (t *Template) ForPlatform(platform string) (Spec, error) {
	var spec Spec
	if err := DecodeJSON(t.Document.Spec, &spec); err != nil {
		return Spec{}, err
	}
	if len(t.Document.SupportedPlatforms) > 0 && !slices.Contains(t.Document.SupportedPlatforms, platform) {
		return Spec{}, fail("PLATFORM_UNSUPPORTED", "The template does not support this Environment platform.")
	}
	if spec.Container != nil && spec.Container.Image == ArtifactPlaceholder {
		artifact, ok := t.Document.PlatformArtifacts[platform]
		if !ok {
			return Spec{}, fail("PLATFORM_UNSUPPORTED", "The template does not declare an image for this Environment platform.")
		}
		if !exactArtifactPattern.MatchString(artifact) {
			return Spec{}, fail("TEMPLATE_ARTIFACT_INVALID", "The platform image does not declare an exact digest.")
		}
		spec.Container.Image = artifact
	}
	if err := ValidateSpec(spec); err != nil {
		return Spec{}, fmt.Errorf("platform specification: %w", err)
	}
	return spec, nil
}
