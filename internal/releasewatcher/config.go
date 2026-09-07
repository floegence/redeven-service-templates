// Package releasewatcher turns verified upstream metadata into declarative catalog updates.
package releasewatcher

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strings"
)

type Config struct {
	SchemaVersion int      `json:"schema_version"`
	CatalogBump   string   `json:"catalog_bump"`
	Sources       []Source `json:"sources"`
}

type Source struct {
	TemplateID          string   `json:"template_id"`
	Kind                string   `json:"kind"`
	PackageName         string   `json:"package_name,omitempty"`
	RegistryURL         string   `json:"registry_url"`
	DistTag             string   `json:"dist_tag,omitempty"`
	RepositoryURL       string   `json:"repository_url,omitempty"`
	RepositoryDirectory string   `json:"repository_directory,omitempty"`
	Repository          string   `json:"repository,omitempty"`
	CanonicalTagPolicy  string   `json:"canonical_tag_policy,omitempty"`
	Platforms           []string `json:"platforms,omitempty"`
}

func LoadConfig(root string) (Config, error) {
	data, err := os.ReadFile(filepath.Join(root, "release_sources.json"))
	if err != nil {
		return Config{}, err
	}
	var config Config
	if err := decodeJSON(data, &config); err != nil {
		return Config{}, err
	}
	return config, config.validate()
}

func (config Config) validate() error {
	if config.SchemaVersion != 1 || config.CatalogBump != "patch" || len(config.Sources) == 0 {
		return errors.New("unsupported release source configuration")
	}
	seen := map[string]bool{}
	for _, source := range config.Sources {
		if !regexp.MustCompile(`^[a-z0-9]+(?:-[a-z0-9]+)*$`).MatchString(source.TemplateID) || seen[source.TemplateID] {
			return errors.New("invalid or duplicate template ID")
		}
		seen[source.TemplateID] = true
		if _, err := registryURL(source.RegistryURL); err != nil {
			return err
		}
		switch source.Kind {
		case "npm":
			if !regexp.MustCompile(`^(?:@[a-z0-9._-]+/)?[a-z0-9._-]+$`).MatchString(source.PackageName) || source.DistTag != "latest" || source.RepositoryURL == "" {
				return errors.New("invalid npm release policy")
			}
		case "oci":
			if !regexp.MustCompile(`^[a-z0-9]+(?:[._-][a-z0-9]+)*(?:/[a-z0-9]+(?:[._-][a-z0-9]+)*)+$`).MatchString(source.Repository) || source.CanonicalTagPolicy != "semver_without_market_suffix" {
				return errors.New("invalid OCI canonical release policy")
			}
			// The current catalog contract requires exactly these two Linux platforms.
			if len(source.Platforms) != 2 || source.Platforms[0] != "linux-amd64" || source.Platforms[1] != "linux-arm64" {
				return errors.New("OCI platforms must be linux-amd64 and linux-arm64")
			}
		default:
			return errors.New("unsupported release source kind")
		}
	}
	return nil
}

func registryURL(raw string) (*url.URL, error) {
	parsed, err := url.Parse(raw)
	if err != nil || parsed.Scheme != "https" || parsed.Host == "" || parsed.User != nil || parsed.RawQuery != "" || parsed.Fragment != "" || (parsed.Path != "" && parsed.Path != "/") {
		return nil, errors.New("Registry URL must be an absolute HTTPS origin")
	}
	return parsed, nil
}

func (source Source) image() string {
	parsed, _ := registryURL(source.RegistryURL)
	return parsed.Host + "/" + source.Repository
}

func decodeJSON(data []byte, target any) error {
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil {
		return err
	}
	if err := decoder.Decode(new(any)); !errors.Is(err, io.EOF) {
		return fmt.Errorf("trailing JSON content: %v", err)
	}
	return nil
}

func trimRegistry(raw string) string { return strings.TrimSuffix(raw, "/") }
