// Package releasewatcher discovers and applies reviewed releases for catalog
// templates. It deliberately owns no runtime lifecycle code; it only turns
// immutable upstream release metadata into declarative catalog data.
package releasewatcher

import (
	"bytes"
	"context"
	"crypto/sha512"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/floegence/redeven-service-templates/internal/cataloggen"
)

const (
	DefaultNPMPackage       = "@deepseek-ai/dsh"
	DefaultNPMRegistry      = "https://registry.npmjs.org/"
	DefaultRepositoryURL    = "git+https://github.com/deepseek-ai/deepseek-harness.git"
	DefaultRepositoryDir    = "apps/cli"
	DefaultGHCRRegistry     = "https://ghcr.io"
	DefaultGHCRRepository   = "runzhliu/deepseek-harness"
	maxMetadataBytes        = 16 << 20
	maxTagCount             = 20_000
	maxManifestBytes        = 2 << 20
	canonicalVersionPattern = `^(?:0|[1-9][0-9]*)\.(?:0|[1-9][0-9]*)\.(?:0|[1-9][0-9]*)(?:-(?:0|[1-9][0-9]*|[0-9A-Za-z-][0-9A-Za-z-]*)(?:\.(?:0|[1-9][0-9]*|[0-9A-Za-z-][0-9A-Za-z-]*))*)?(?:\+[0-9A-Za-z-]+(?:\.[0-9A-Za-z-]+)*)?$`
)

var canonicalVersionRE = regexp.MustCompile(canonicalVersionPattern)
var digestRE = regexp.MustCompile(`^sha256:[0-9a-f]{64}$`)

// Sources describes the upstream coordinates. The defaults are the only
// production sources; overrides make the network boundary deterministic in
// tests and allow an operator to use a read-only mirror.
type Sources struct {
	NPMPackage     string
	NPMRegistry    string
	RepositoryURL  string
	RepositoryDir  string
	GHCRRegistry   string
	GHCRRepository string
}

type Config struct {
	SchemaVersion int `json:"schema_version"`
	Sources       []struct {
		TemplateID          string   `json:"template_id"`
		Kind                string   `json:"kind"`
		PackageName         string   `json:"package_name"`
		RegistryURL         string   `json:"registry_url"`
		DistTag             string   `json:"dist_tag"`
		RepositoryURL       string   `json:"repository_url"`
		RepositoryDirectory string   `json:"repository_directory"`
		Repository          string   `json:"repository"`
		CanonicalTagPolicy  string   `json:"canonical_tag_policy"`
		Platforms           []string `json:"platforms"`
	} `json:"sources"`
}

// LoadConfig validates the declarative source map used by the scheduled
// watcher. Runtime template definitions remain the sole lifecycle authority.
func LoadConfig(root string) (Sources, error) {
	data, err := os.ReadFile(filepath.Join(root, "release_sources.json"))
	if err != nil {
		return Sources{}, err
	}
	var config Config
	if err := decodeJSON(data, &config); err != nil {
		return Sources{}, fmt.Errorf("decode release_sources.json: %w", err)
	}
	if config.SchemaVersion != 1 || len(config.Sources) != 2 {
		return Sources{}, errors.New("release source configuration is unsupported")
	}
	var result Sources
	for _, source := range config.Sources {
		switch source.Kind {
		case "npm":
			if source.TemplateID != "deepseek-harness-host" || source.DistTag != "latest" || source.PackageName == "" || source.RegistryURL == "" || source.RepositoryURL == "" || source.RepositoryDirectory == "" {
				return Sources{}, errors.New("npm release source configuration is invalid")
			}
			result.NPMPackage, result.NPMRegistry = source.PackageName, source.RegistryURL
			result.RepositoryURL, result.RepositoryDir = source.RepositoryURL, source.RepositoryDirectory
		case "oci":
			if source.TemplateID != "deepseek-harness-container" || source.CanonicalTagPolicy != "semver_without_market_suffix" || source.RegistryURL == "" || source.Repository == "" || len(source.Platforms) != 2 || source.Platforms[0] != "linux-amd64" || source.Platforms[1] != "linux-arm64" {
				return Sources{}, errors.New("OCI release source configuration is invalid")
			}
			result.GHCRRegistry, result.GHCRRepository = source.RegistryURL, source.Repository
		default:
			return Sources{}, errors.New("release source kind is unsupported")
		}
	}
	if result.NPMPackage == "" || result.GHCRRepository == "" {
		return Sources{}, errors.New("release source configuration is incomplete")
	}
	return result, nil
}

func DefaultSources() Sources {
	return Sources{
		NPMPackage: DefaultNPMPackage, NPMRegistry: DefaultNPMRegistry,
		RepositoryURL: DefaultRepositoryURL, RepositoryDir: DefaultRepositoryDir,
		GHCRRegistry: DefaultGHCRRegistry, GHCRRepository: DefaultGHCRRepository,
	}
}

func DiscoverRepository(ctx context.Context, root string, client *http.Client) (ReleasePlan, error) {
	sources, err := LoadConfig(root)
	if err != nil {
		return ReleasePlan{}, err
	}
	return Discover(ctx, Options{Client: client, Sources: sources})
}

// ReleasePlan is the fully verified, immutable input to Apply.
type ReleasePlan struct {
	HostVersion        string
	HostIntegrity      string
	ContainerVersion   string
	ContainerArtifacts map[string]string
	CatalogVersion     string
}

// Options controls discovery. Client must be HTTPS-capable; a custom client
// is useful for tests and should not weaken the production source checks.
type Options struct {
	Client  *http.Client
	Sources Sources
}

// Discover verifies both upstream release sources without changing files.
func Discover(ctx context.Context, options Options) (ReleasePlan, error) {
	sources := options.Sources
	if sources.NPMPackage == "" {
		sources = DefaultSources()
	}
	client := options.Client
	if client == nil {
		client = &http.Client{Timeout: 30 * time.Second}
	}
	host, err := discoverNPM(ctx, client, sources)
	if err != nil {
		return ReleasePlan{}, err
	}
	container, err := discoverOCI(ctx, client, sources)
	if err != nil {
		return ReleasePlan{}, err
	}
	return ReleasePlan{
		HostVersion: host.Version, HostIntegrity: host.Integrity,
		ContainerVersion: container.Tag, ContainerArtifacts: container.Artifacts,
	}, nil
}

// Apply updates only when the verified plan differs from the current catalog.
// All source and generated bytes are prepared before the first destination is
// replaced. A no-op leaves every file untouched.
func Apply(root string, plan ReleasePlan) (bool, error) {
	if !validVersion(plan.HostVersion) || !validNPMIntegrity(plan.HostIntegrity) || !validVersion(plan.ContainerVersion) || !validArtifactSet(plan.ContainerVersion, plan.ContainerArtifacts) {
		return false, errors.New("release plan is incomplete")
	}
	hostPath := filepath.Join(root, "templates", "deepseek-harness-host", "template.json")
	containerPath := filepath.Join(root, "templates", "deepseek-harness-container", "template.json")
	hostSource, host, err := readDefinition(hostPath)
	if err != nil {
		return false, err
	}
	containerSource, container, err := readDefinition(containerPath)
	if err != nil {
		return false, err
	}
	if host.TemplateID != "deepseek-harness-host" || container.TemplateID != "deepseek-harness-container" {
		return false, errors.New("release template identity does not match the configured sources")
	}
	changed := host.RecommendedVersion != plan.HostVersion || hostRevision(host) < 7 || npmVersion(host) != plan.HostVersion
	changed = changed || container.RecommendedVersion != plan.ContainerVersion || containerRevision(container) < 4 || !artifactsEqual(container, plan.ContainerArtifacts)
	if !changed {
		return false, nil
	}

	hostRevision := maxInt(host.Revision+1, 7)
	containerRevision := maxInt(container.Revision+1, 4)
	hostBytes, err := patchHostDefinition(hostSource, host, plan.HostVersion, hostRevision)
	if err != nil {
		return false, err
	}
	containerBytes, err := patchContainerDefinition(containerSource, container, plan.ContainerVersion, containerRevision, plan.ContainerArtifacts)
	if err != nil {
		return false, err
	}

	currentCatalogVersion, err := readCatalogVersion(root)
	if err != nil {
		return false, err
	}
	nextCatalogVersion, err := bumpPatchVersion(currentCatalogVersion)
	if err != nil {
		return false, err
	}
	plan.CatalogVersion = nextCatalogVersion

	// Generate from the candidate definitions before touching the checkout. The
	// generator receives the candidate catalog version explicitly because the
	// source constants are changed in the same transaction.
	stagedRoot, err := os.MkdirTemp("", "redeven-service-templates-release-")
	if err != nil {
		return false, err
	}
	defer os.RemoveAll(stagedRoot)
	if err := copyTree(root, stagedRoot); err != nil {
		return false, fmt.Errorf("stage catalog: %w", err)
	}
	if err := os.WriteFile(filepath.Join(stagedRoot, "templates", "deepseek-harness-host", "template.json"), hostBytes, 0o644); err != nil {
		return false, err
	}
	if err := os.WriteFile(filepath.Join(stagedRoot, "templates", "deepseek-harness-container", "template.json"), containerBytes, 0o644); err != nil {
		return false, err
	}
	if err := updateCatalogConstants(stagedRoot, nextCatalogVersion); err != nil {
		return false, err
	}
	output, err := cataloggen.GenerateWithVersion(stagedRoot, nextCatalogVersion)
	if err != nil {
		return false, fmt.Errorf("generate catalog: %w", err)
	}
	if err := cataloggen.Write(stagedRoot, output); err != nil {
		return false, fmt.Errorf("write staged catalog: %w", err)
	}
	if err := cataloggen.Verify(stagedRoot, output); err != nil {
		return false, fmt.Errorf("verify staged catalog: %w", err)
	}

	for _, relative := range []string{
		"templates/deepseek-harness-host/template.json",
		"templates/deepseek-harness-container/template.json",
		"catalog.go", "internal/cataloggen/generate.go",
		"dist/catalog.bundle.json", "dist/catalog.bundle.manifest.json", "dist/catalog.bundle.sha256",
	} {
		if err := atomicReplace(filepath.Join(stagedRoot, relative), filepath.Join(root, relative)); err != nil {
			return false, fmt.Errorf("apply %s: %w", relative, err)
		}
	}
	return true, nil
}

type npmRelease struct {
	Version   string
	Integrity string
}

type npmPackument struct {
	Name     string            `json:"name"`
	DistTags map[string]string `json:"dist-tags"`
	Versions map[string]struct {
		Version    string `json:"version"`
		Deprecated any    `json:"deprecated"`
		Repository struct {
			URL       string `json:"url"`
			Type      string `json:"type"`
			Directory string `json:"directory"`
		} `json:"repository"`
		Dist struct {
			Integrity string `json:"integrity"`
		} `json:"dist"`
	} `json:"versions"`
}

func discoverNPM(ctx context.Context, client *http.Client, sources Sources) (npmRelease, error) {
	endpoint, err := packageEndpoint(sources.NPMRegistry, sources.NPMPackage)
	if err != nil {
		return npmRelease{}, err
	}
	data, err := fetchJSON(ctx, client, endpoint, "npm")
	if err != nil {
		return npmRelease{}, err
	}
	var packument npmPackument
	if err := json.Unmarshal(data, &packument); err != nil {
		return npmRelease{}, fmt.Errorf("decode npm metadata: %w", err)
	}
	if packument.Name != sources.NPMPackage {
		return npmRelease{}, errors.New("npm package name does not match configured source")
	}
	version := strings.TrimSpace(packument.DistTags["latest"])
	if !validVersion(version) {
		return npmRelease{}, errors.New("npm latest dist-tag is missing or invalid")
	}
	document, ok := packument.Versions[version]
	if !ok || strings.TrimSpace(document.Version) != version {
		return npmRelease{}, errors.New("npm latest package metadata is missing")
	}
	if deprecated(document.Deprecated) {
		return npmRelease{}, errors.New("npm latest package is deprecated")
	}
	if document.Repository.Type != "git" || strings.TrimSpace(document.Repository.URL) != sources.RepositoryURL || strings.TrimSpace(document.Repository.Directory) != sources.RepositoryDir {
		return npmRelease{}, errors.New("npm package repository does not match the official DeepSeek Harness source")
	}
	if !validNPMIntegrity(document.Dist.Integrity) {
		return npmRelease{}, errors.New("npm latest package has no valid SHA-512 integrity")
	}
	return npmRelease{Version: version, Integrity: strings.TrimSpace(document.Dist.Integrity)}, nil
}

type ociRelease struct {
	Tag       string
	Artifacts map[string]string
}

func discoverOCI(ctx context.Context, client *http.Client, sources Sources) (ociRelease, error) {
	base, err := registryBaseURL(sources.GHCRRegistry)
	if err != nil {
		return ociRelease{}, err
	}
	tokenURL := base + "/token?service=ghcr.io&scope=repository:" + url.QueryEscape(sources.GHCRRepository) + ":pull"
	tokenData, err := fetchJSON(ctx, client, tokenURL, "GHCR token")
	if err != nil {
		return ociRelease{}, err
	}
	var token struct {
		Token string `json:"token"`
	}
	if err := json.Unmarshal(tokenData, &token); err != nil || strings.TrimSpace(token.Token) == "" {
		return ociRelease{}, errors.New("GHCR bearer token is missing")
	}
	tagsData, err := fetchAuthorized(ctx, client, base+"/v2/"+sources.GHCRRepository+"/tags/list", token.Token, "GHCR tags")
	if err != nil {
		return ociRelease{}, err
	}
	var tags struct {
		Tags []string `json:"tags"`
	}
	if err := json.Unmarshal(tagsData, &tags); err != nil || len(tags.Tags) == 0 || len(tags.Tags) > maxTagCount {
		return ociRelease{}, errors.New("GHCR tag response is invalid")
	}
	candidates := make([]string, 0, len(tags.Tags))
	for _, tag := range tags.Tags {
		tag = strings.TrimSpace(tag)
		if validVersion(tag) && !strings.Contains(tag, "-market.") {
			candidates = append(candidates, tag)
		}
	}
	if len(candidates) == 0 {
		return ociRelease{}, errors.New("GHCR has no canonical SemVer release tag")
	}
	sort.Slice(candidates, func(i, j int) bool {
		comparison := compareVersions(candidates[i], candidates[j])
		if comparison == 0 {
			return candidates[i] > candidates[j]
		}
		return comparison > 0
	})
	tag := candidates[0]
	manifestData, err := fetchAuthorizedWithAccept(ctx, client, base+"/v2/"+sources.GHCRRepository+"/manifests/"+url.PathEscape(tag), token.Token, "GHCR manifest")
	if err != nil {
		return ociRelease{}, err
	}
	var index struct {
		SchemaVersion int `json:"schemaVersion"`
		Manifests     []struct {
			Digest   string `json:"digest"`
			Platform *struct {
				OS           string `json:"os"`
				Architecture string `json:"architecture"`
			} `json:"platform"`
		} `json:"manifests"`
	}
	if err := json.Unmarshal(manifestData, &index); err != nil || index.SchemaVersion != 2 || len(index.Manifests) == 0 {
		return ociRelease{}, errors.New("GHCR release is not a manifest index")
	}
	artifacts := map[string]string{}
	for _, descriptor := range index.Manifests {
		if descriptor.Platform == nil || descriptor.Platform.OS != "linux" || !digestRE.MatchString(descriptor.Digest) {
			continue
		}
		key := "linux-" + descriptor.Platform.Architecture
		if key == "linux-amd64" || key == "linux-arm64" {
			if _, exists := artifacts[key]; exists {
				return ociRelease{}, errors.New("GHCR manifest has duplicate platform descriptors")
			}
			artifacts[key] = baseImage(sources.GHCRRepository) + ":" + tag + "@" + descriptor.Digest
		}
	}
	if len(artifacts) != 2 {
		return ociRelease{}, errors.New("GHCR release does not provide linux amd64 and arm64 descriptors")
	}
	return ociRelease{Tag: tag, Artifacts: artifacts}, nil
}

func baseImage(repository string) string {
	return "ghcr.io/" + strings.TrimPrefix(strings.TrimSpace(repository), "/")
}

func packageEndpoint(registry, packageName string) (string, error) {
	parsed, err := url.Parse(strings.TrimSpace(registry))
	if err != nil || parsed.Scheme != "https" || parsed.Host == "" || parsed.User != nil || parsed.RawQuery != "" || parsed.Fragment != "" {
		return "", errors.New("npm Registry URL must be an absolute HTTPS URL")
	}
	parsed.Path = strings.TrimRight(parsed.Path, "/") + "/" + packageName
	parsed.RawPath = ""
	return parsed.String(), nil
}

func registryBaseURL(registry string) (string, error) {
	parsed, err := url.Parse(strings.TrimSpace(registry))
	if err != nil || parsed.Scheme != "https" || parsed.Host == "" || parsed.User != nil || parsed.RawQuery != "" || parsed.Fragment != "" {
		return "", errors.New("OCI Registry URL must be an absolute HTTPS URL")
	}
	parsed.Path = strings.TrimRight(parsed.Path, "/")
	parsed.RawPath = ""
	return strings.TrimRight(parsed.String(), "/"), nil
}

func fetchJSON(ctx context.Context, client *http.Client, endpoint, source string) ([]byte, error) {
	return fetch(ctx, client, endpoint, "", source, maxMetadataBytes)
}

func fetchAuthorized(ctx context.Context, client *http.Client, endpoint, token, source string) ([]byte, error) {
	return fetch(ctx, client, endpoint, token, source, maxMetadataBytes)
}

func fetchAuthorizedWithAccept(ctx context.Context, client *http.Client, endpoint, token, source string) ([]byte, error) {
	return fetch(ctx, client, endpoint, token, source, maxManifestBytes)
}

func fetch(ctx context.Context, client *http.Client, endpoint, token, source string, limit int64) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return nil, fmt.Errorf("%s request: %w", source, err)
	}
	req.Header.Set("Accept", "application/json, application/vnd.oci.image.index.v1+json, application/vnd.docker.distribution.manifest.list.v2+json")
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	resp, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("%s request failed: %w", source, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, fmt.Errorf("%s returned HTTP %d", source, resp.StatusCode)
	}
	data, err := io.ReadAll(io.LimitReader(resp.Body, limit+1))
	if err != nil {
		return nil, fmt.Errorf("read %s response: %w", source, err)
	}
	if int64(len(data)) > limit {
		return nil, fmt.Errorf("%s response exceeds the safe metadata limit", source)
	}
	return data, nil
}

func decodeJSON(data []byte, destination any) error {
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(destination); err != nil {
		return err
	}
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		if err == nil {
			return errors.New("trailing JSON value")
		}
		return err
	}
	return nil
}

func validNPMIntegrity(value string) bool {
	algorithm, encoded, ok := strings.Cut(strings.TrimSpace(value), "-")
	if !ok || algorithm != "sha512" {
		return false
	}
	digest, err := base64.StdEncoding.DecodeString(encoded)
	return err == nil && len(digest) == sha512.Size
}

func deprecated(value any) bool {
	text := strings.TrimSpace(fmt.Sprint(value))
	return value != nil && text != "" && text != "false" && text != "<nil>"
}

func validVersion(value string) bool {
	return canonicalVersionRE.MatchString(strings.TrimSpace(value)) && parseVersion(value) != nil
}

type semVersion struct {
	major, minor, patch int64
	pre                 []string
}

func parseVersion(value string) *semVersion {
	value = strings.TrimSpace(value)
	value = strings.SplitN(value, "+", 2)[0]
	parts := strings.SplitN(value, "-", 2)
	core := strings.Split(parts[0], ".")
	if len(core) != 3 {
		return nil
	}
	result := &semVersion{}
	for index, part := range core {
		number, err := strconv.ParseInt(part, 10, 64)
		if err != nil || number < 0 {
			return nil
		}
		switch index {
		case 0:
			result.major = number
		case 1:
			result.minor = number
		default:
			result.patch = number
		}
	}
	if len(parts) == 2 {
		result.pre = strings.Split(parts[1], ".")
		for _, item := range result.pre {
			if item == "" || (allDigits(item) && len(item) > 1 && item[0] == '0') {
				return nil
			}
		}
	}
	return result
}

func allDigits(value string) bool {
	if value == "" {
		return false
	}
	for _, character := range value {
		if character < '0' || character > '9' {
			return false
		}
	}
	return true
}

func compareVersions(left, right string) int {
	a, b := parseVersion(left), parseVersion(right)
	if a == nil || b == nil {
		return strings.Compare(left, right)
	}
	for _, pair := range [][2]int64{{a.major, b.major}, {a.minor, b.minor}, {a.patch, b.patch}} {
		if pair[0] != pair[1] {
			if pair[0] > pair[1] {
				return 1
			}
			return -1
		}
	}
	if len(a.pre) == 0 && len(b.pre) == 0 {
		return 0
	}
	if len(a.pre) == 0 {
		return 1
	}
	if len(b.pre) == 0 {
		return -1
	}
	for i := 0; i < len(a.pre) && i < len(b.pre); i++ {
		leftItem, rightItem := a.pre[i], b.pre[i]
		leftNum, leftErr := strconv.ParseInt(leftItem, 10, 64)
		rightNum, rightErr := strconv.ParseInt(rightItem, 10, 64)
		if leftErr == nil && rightErr == nil {
			if leftNum != rightNum {
				if leftNum > rightNum {
					return 1
				}
				return -1
			}
			continue
		}
		if leftErr == nil {
			return -1
		}
		if rightErr == nil {
			return 1
		}
		if leftItem != rightItem {
			return strings.Compare(leftItem, rightItem)
		}
	}
	if len(a.pre) == len(b.pre) {
		return 0
	}
	if len(a.pre) > len(b.pre) {
		return 1
	}
	return -1
}

func readDefinition(path string) ([]byte, cataloggen.TemplateDefinition, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, cataloggen.TemplateDefinition{}, err
	}
	var definition cataloggen.TemplateDefinition
	if err := decodeJSON(data, &definition); err != nil {
		return nil, cataloggen.TemplateDefinition{}, fmt.Errorf("decode %s: %w", path, err)
	}
	return data, definition, nil
}

func hostRevision(def cataloggen.TemplateDefinition) int      { return def.Revision }
func containerRevision(def cataloggen.TemplateDefinition) int { return def.Revision }

func npmVersion(def cataloggen.TemplateDefinition) string {
	var spec struct {
		Host struct {
			NPM struct {
				Version string `json:"version"`
			} `json:"npm"`
		} `json:"host"`
	}
	_ = json.Unmarshal(def.Spec, &spec)
	return spec.Host.NPM.Version
}

func patchHostDefinition(source []byte, current cataloggen.TemplateDefinition, version string, revision int) ([]byte, error) {
	updated, err := replaceSingle(source, regexp.MustCompile(`(?m)^  "recommended_version": "[^"]+",$`), []byte(`  "recommended_version": `+quoteJSON(version)+`,`))
	if err != nil {
		return nil, fmt.Errorf("patch host recommended version: %w", err)
	}
	updated, err = replaceSingle(updated, regexp.MustCompile(`(?s)("recommended_version"\s*:\s*"[^"]+"\s*,\s*"revision"\s*:\s*)[0-9]+`), []byte(`${1}`+strconv.Itoa(revision)))
	if err != nil {
		return nil, fmt.Errorf("patch host revision: %w", err)
	}
	updated, err = replaceObjectStringField(updated, "npm", "version", version)
	if err != nil {
		return nil, fmt.Errorf("patch host npm version: %w", err)
	}
	var candidate cataloggen.TemplateDefinition
	if err := decodeJSON(updated, &candidate); err != nil {
		return nil, fmt.Errorf("validate patched host template: %w", err)
	}
	if candidate.TemplateID != current.TemplateID || candidate.RecommendedVersion != version || candidate.Revision != revision || npmVersion(candidate) != version {
		return nil, errors.New("patched host template does not match the release plan")
	}
	return updated, nil
}

func patchContainerDefinition(source []byte, current cataloggen.TemplateDefinition, version string, revision int, artifacts map[string]string) ([]byte, error) {
	updated, err := replaceSingle(source, regexp.MustCompile(`(?m)^  "recommended_version": "[^"]+",$`), []byte(`  "recommended_version": `+quoteJSON(version)+`,`))
	if err != nil {
		return nil, fmt.Errorf("patch container recommended version: %w", err)
	}
	updated, err = replaceSingle(updated, regexp.MustCompile(`(?s)("recommended_version"\s*:\s*"[^"]+"\s*,\s*"revision"\s*:\s*)[0-9]+`), []byte(`${1}`+strconv.Itoa(revision)))
	if err != nil {
		return nil, fmt.Errorf("patch container revision: %w", err)
	}
	for _, platform := range []string{"linux-amd64", "linux-arm64"} {
		updated, err = replaceObjectStringField(updated, "platform_artifacts", platform, artifacts[platform])
		if err != nil {
			return nil, fmt.Errorf("patch container %s artifact: %w", platform, err)
		}
	}
	var candidate cataloggen.TemplateDefinition
	if err := decodeJSON(updated, &candidate); err != nil {
		return nil, fmt.Errorf("validate patched container template: %w", err)
	}
	if candidate.TemplateID != current.TemplateID || candidate.RecommendedVersion != version || candidate.Revision != revision || !artifactsEqual(candidate, artifacts) {
		return nil, errors.New("patched container template does not match the release plan")
	}
	return updated, nil
}

func replaceObjectStringField(source []byte, objectKey, field, value string) ([]byte, error) {
	objectPattern := regexp.MustCompile(`(?s)"` + regexp.QuoteMeta(objectKey) + `"\s*:\s*\{[^{}]*\}`)
	locations := objectPattern.FindAllIndex(source, -1)
	if len(locations) != 1 {
		return nil, fmt.Errorf("expected one %s object, found %d", objectKey, len(locations))
	}
	start, end := locations[0][0], locations[0][1]
	object := source[start:end]
	fieldPattern := regexp.MustCompile(`("` + regexp.QuoteMeta(field) + `"\s*:\s*)"[^"]*"`)
	replaced, err := replaceSingle(object, fieldPattern, []byte(`${1}`+quoteJSON(value)))
	if err != nil {
		return nil, err
	}
	result := make([]byte, 0, len(source)-len(object)+len(replaced))
	result = append(result, source[:start]...)
	result = append(result, replaced...)
	result = append(result, source[end:]...)
	return result, nil
}

func replaceSingle(source []byte, pattern *regexp.Regexp, replacement []byte) ([]byte, error) {
	if count := len(pattern.FindAllIndex(source, -1)); count != 1 {
		return nil, fmt.Errorf("expected one match, found %d", count)
	}
	return pattern.ReplaceAll(source, replacement), nil
}

func quoteJSON(value string) string {
	encoded, _ := json.Marshal(value)
	return string(encoded)
}

func artifactsEqual(def cataloggen.TemplateDefinition, want map[string]string) bool {
	if len(def.PlatformArtifacts) != len(want) {
		return false
	}
	for key, value := range want {
		if def.PlatformArtifacts[key] != value {
			return false
		}
	}
	return true
}

func validArtifactSet(version string, artifacts map[string]string) bool {
	if len(artifacts) != 2 {
		return false
	}
	prefix := "ghcr.io/" + DefaultGHCRRepository + ":" + version + "@"
	for _, platform := range []string{"linux-amd64", "linux-arm64"} {
		artifact, ok := artifacts[platform]
		if !ok || !strings.HasPrefix(artifact, prefix) || !digestRE.MatchString(strings.TrimPrefix(artifact, prefix)) {
			return false
		}
	}
	return true
}

func maxInt(left, right int) int {
	if left > right {
		return left
	}
	return right
}

func readCatalogVersion(root string) (string, error) {
	var version string
	for _, source := range []struct {
		path, pattern string
	}{
		{"catalog.go", `const Version = "(v[0-9]+\.[0-9]+\.[0-9]+)"`},
		{"internal/cataloggen/generate.go", `const CatalogVersion = "(v[0-9]+\.[0-9]+\.[0-9]+)"`},
	} {
		data, err := os.ReadFile(filepath.Join(root, source.path))
		if err != nil {
			return "", err
		}
		match := regexp.MustCompile(source.pattern).FindSubmatch(data)
		if len(match) != 2 {
			return "", fmt.Errorf("catalog version constant is missing in %s", source.path)
		}
		candidate := string(match[1])
		if version != "" && candidate != version {
			return "", errors.New("catalog version constants do not match")
		}
		version = candidate
	}
	return version, nil
}

func bumpPatchVersion(value string) (string, error) {
	value = strings.TrimPrefix(strings.TrimSpace(value), "v")
	parts := strings.Split(value, ".")
	if len(parts) != 3 {
		return "", errors.New("catalog version is not semver")
	}
	patch, err := strconv.Atoi(parts[2])
	if err != nil {
		return "", err
	}
	return fmt.Sprintf("v%s.%s.%d", parts[0], parts[1], patch+1), nil
}

func updateCatalogConstants(root, version string) error {
	for _, relative := range []string{"catalog.go", "internal/cataloggen/generate.go"} {
		path := filepath.Join(root, relative)
		data, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		pattern := regexp.MustCompile(`(const (?:Version|CatalogVersion) = ")v[0-9]+\.[0-9]+\.[0-9]+(")`)
		updated := pattern.ReplaceAll(data, []byte("${1}"+version+"${2}"))
		if bytes.Equal(data, updated) {
			return fmt.Errorf("catalog version constant missing in %s", relative)
		}
		if err := os.WriteFile(path, updated, 0o644); err != nil {
			return err
		}
	}
	return nil
}

func copyTree(source, destination string) error {
	return filepath.Walk(source, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		relative, err := filepath.Rel(source, path)
		if err != nil {
			return err
		}
		if relative == ".git" || strings.HasPrefix(relative, ".git"+string(filepath.Separator)) {
			if info.IsDir() {
				return filepath.SkipDir
			}
			return nil
		}
		target := filepath.Join(destination, relative)
		if info.IsDir() {
			return os.MkdirAll(target, info.Mode().Perm())
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		return os.WriteFile(target, data, info.Mode().Perm())
	})
}

func atomicReplace(source, destination string) error {
	data, err := os.ReadFile(source)
	if err != nil {
		return err
	}
	temporary, err := os.CreateTemp(filepath.Dir(destination), ".release-watcher-*")
	if err != nil {
		return err
	}
	temporaryName := temporary.Name()
	defer os.Remove(temporaryName)
	if _, err := temporary.Write(data); err != nil {
		temporary.Close()
		return err
	}
	if err := temporary.Chmod(0o644); err != nil {
		temporary.Close()
		return err
	}
	if err := temporary.Close(); err != nil {
		return err
	}
	return os.Rename(temporaryName, destination)
}
