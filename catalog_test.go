package servicetemplates

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"testing"
)

func TestEmbeddedBundleIntegrity(t *testing.T) {
	data := Bundle()
	sum := sha256.Sum256(data)
	if got := hex.EncodeToString(sum[:]); got != BundleSHA256() {
		t.Fatalf("bundle digest = %s, want %s", got, BundleSHA256())
	}
	data[0] ^= 0xff
	if string(data) == string(Bundle()) {
		t.Fatal("Bundle returned shared mutable storage")
	}
}

func TestEmbeddedBundleShape(t *testing.T) {
	var bundle struct {
		SchemaVersion  int      `json:"schema_version"`
		CatalogVersion string   `json:"catalog_version"`
		Locales        []string `json:"locales"`
		Templates      []struct {
			RecommendedVersion string          `json:"recommended_version"`
			Version            json.RawMessage `json:"version"`
			Spec               struct {
				SchemaVersion int `json:"schema_version"`
			} `json:"spec"`
			Localizations map[string]json.RawMessage `json:"localizations"`
		} `json:"templates"`
	}
	if err := json.Unmarshal(Bundle(), &bundle); err != nil {
		t.Fatal(err)
	}
	if bundle.SchemaVersion != 2 || bundle.CatalogVersion != Version || len(bundle.Templates) != 4 || len(bundle.Locales) != 10 {
		t.Fatalf("unexpected catalog shape: %+v", bundle)
	}
	for index, template := range bundle.Templates {
		if template.RecommendedVersion == "" || len(template.Version) != 0 || template.Spec.SchemaVersion != 6 {
			t.Fatalf("template %d recommendation contract is invalid: %+v", index, template)
		}
		if len(template.Localizations) != len(bundle.Locales) {
			t.Fatalf("template %d has %d localizations, want %d", index, len(template.Localizations), len(bundle.Locales))
		}
	}
}
