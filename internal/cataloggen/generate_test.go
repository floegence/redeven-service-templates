package cataloggen

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestCommittedCatalogIsCurrent(t *testing.T) {
	root := filepath.Clean(filepath.Join("..", ".."))
	if err := HasForbiddenCompatibilityCode(root); err != nil {
		t.Fatal(err)
	}
	output, err := Generate(root)
	if err != nil {
		t.Fatal(err)
	}
	if err := Verify(root, output); err != nil {
		t.Fatal(err)
	}
}

func TestCompatibilityDirectoryIsRejected(t *testing.T) {
	root := t.TempDir()
	if err := os.Mkdir(filepath.Join(root, "compatibility"), 0o755); err != nil {
		t.Fatal(err)
	}
	if _, err := Generate(root); err == nil {
		t.Fatal("Generate accepted a compatibility directory")
	}
}

func TestRecommendedVersionMustMatchRuntimeDefault(t *testing.T) {
	t.Parallel()
	host := TemplateDefinition{
		SchemaVersion: 2, TemplateID: "example-host", ServiceFamilyID: "example-host", RecommendedVersion: "1.2.3",
		Revision: 1, DiskBytes: 1, SourceURL: "https://example.com/source", Deployment: "host",
		SupportedPlatforms: []string{"darwin-arm64"}, ReleaseDiscovery: &ReleaseDiscovery{Source: "npm"},
		Icon: IconReference{Path: "assets/icon.svg", MediaType: "image/svg+xml"},
		Spec: json.RawMessage(`{"schema_version":4,"kind":"host","host":{"npm":{"version":"1.2.3"}}}`),
	}
	if err := validateDefinition("example-host", host); err != nil {
		t.Fatal(err)
	}
	host.RecommendedVersion = "2.0.0"
	if err := validateDefinition("example-host", host); err == nil || !strings.Contains(err.Error(), "recommended version") {
		t.Fatalf("mismatched Host recommendation error = %v", err)
	}

	container := TemplateDefinition{
		SchemaVersion: 2, TemplateID: "example-container", ServiceFamilyID: "example-container", RecommendedVersion: "1.2.3",
		Revision: 1, DiskBytes: 1, SourceURL: "https://example.com/source", Deployment: "container", ContainerMode: "single",
		PlatformArtifacts: map[string]string{"linux-amd64": "registry.example/app:1.2.3@sha256:" + strings.Repeat("a", 64)},
		ReleaseDiscovery:  &ReleaseDiscovery{Source: "oci"}, Icon: IconReference{Path: "assets/icon.svg", MediaType: "image/svg+xml"},
		Spec: json.RawMessage(`{"schema_version":4,"kind":"container","container":{"image":"${REDEVEN_CATALOG_ARTIFACT}"}}`),
	}
	if err := validateDefinition("example-container", container); err != nil {
		t.Fatal(err)
	}
	container.Spec = json.RawMessage(`{"schema_version":4,"kind":"container","container":{"image":"${REDEVEN_CATALOG_ARTIFACT}","release_policy":{"blocked_tag_prefixes":["internal"]}}}`)
	if err := validateDefinition("example-container", container); err == nil || !strings.Contains(err.Error(), "cannot restrict") {
		t.Fatalf("release restriction error = %v", err)
	}
}
