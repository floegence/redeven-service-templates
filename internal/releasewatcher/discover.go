package releasewatcher

import (
	"context"
	"crypto/sha256"
	"crypto/sha512"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"sort"
	"strings"
	"time"
)

const maxMetadataBytes = 16 << 20
const maxTagCount = 20000
const manifestAccept = "application/vnd.oci.image.index.v1+json, application/vnd.docker.distribution.manifest.list.v2+json, application/vnd.oci.image.manifest.v1+json, application/vnd.docker.distribution.manifest.v2+json"

var digestRE = regexp.MustCompile(`^sha256:[0-9a-f]{64}$`)

type Release struct {
	Source    Source
	Version   string
	Integrity string
	Artifacts map[string]string
}
type ReleasePlan struct{ Releases []Release }

func DiscoverRepository(ctx context.Context, root string, client *http.Client) (ReleasePlan, error) {
	config, err := LoadConfig(root)
	if err != nil {
		return ReleasePlan{}, err
	}
	return discover(ctx, config, client)
}

func discover(ctx context.Context, config Config, client *http.Client) (ReleasePlan, error) {
	if err := config.validate(); err != nil {
		return ReleasePlan{}, err
	}
	if client == nil {
		client = &http.Client{Timeout: 30 * time.Second}
	}
	safeClient := *client
	// Metadata and tokens never leave the configured HTTPS origin via redirects.
	safeClient.CheckRedirect = func(*http.Request, []*http.Request) error { return errors.New("Registry redirects are not supported") }
	plan := ReleasePlan{}
	for _, source := range config.Sources {
		var release Release
		var err error
		if source.Kind == "npm" {
			release, err = discoverNPM(ctx, &safeClient, source)
		} else {
			release, err = discoverOCI(ctx, &safeClient, source)
		}
		if err != nil {
			return ReleasePlan{}, fmt.Errorf("%s: %w", source.TemplateID, err)
		}
		plan.Releases = append(plan.Releases, release)
	}
	return plan, nil
}

func discoverNPM(ctx context.Context, client *http.Client, source Source) (Release, error) {
	endpoint := trimRegistry(source.RegistryURL) + "/" + url.PathEscape(source.PackageName)
	data, _, err := fetch(ctx, client, endpoint, "", maxMetadataBytes)
	if err != nil {
		return Release{}, err
	}
	var packument struct {
		Name       string            `json:"name"`
		Deprecated json.RawMessage   `json:"deprecated"`
		DistTags   map[string]string `json:"dist-tags"`
		Versions   map[string]struct {
			Name       string                                `json:"name"`
			Version    string                                `json:"version"`
			Deprecated json.RawMessage                       `json:"deprecated"`
			Repository struct{ Type, URL, Directory string } `json:"repository"`
			Dist       struct{ Integrity string }            `json:"dist"`
		} `json:"versions"`
	}
	if err := json.Unmarshal(data, &packument); err != nil {
		return Release{}, fmt.Errorf("invalid npm metadata: %w", err)
	}
	if packument.Name != source.PackageName {
		return Release{}, errors.New("npm package name mismatch")
	}
	version := packument.DistTags[source.DistTag]
	if !validVersion(version) {
		return Release{}, errors.New("npm latest dist-tag is missing or invalid")
	}
	document, ok := packument.Versions[version]
	if !ok || document.Version != version || document.Name != source.PackageName {
		return Release{}, errors.New("npm version identity mismatch")
	}
	if deprecated(packument.Deprecated) || deprecated(document.Deprecated) {
		return Release{}, errors.New("npm latest package is deprecated")
	}
	if document.Repository.Type != "git" || document.Repository.URL != source.RepositoryURL || document.Repository.Directory != source.RepositoryDirectory {
		return Release{}, errors.New("npm repository mismatch")
	}
	if !validNPMIntegrity(document.Dist.Integrity) {
		return Release{}, errors.New("npm SHA-512 integrity is missing or invalid")
	}
	return Release{Source: source, Version: version, Integrity: document.Dist.Integrity}, nil
}

func deprecated(value json.RawMessage) bool {
	return len(value) > 0 && string(value) != "null" && string(value) != `""` && string(value) != "false"
}
func validNPMIntegrity(value string) bool {
	digest, err := base64.StdEncoding.DecodeString(strings.TrimPrefix(value, "sha512-"))
	return strings.HasPrefix(value, "sha512-") && err == nil && len(digest) == sha512.Size
}

func discoverOCI(ctx context.Context, client *http.Client, source Source) (Release, error) {
	parsed, err := registryURL(source.RegistryURL)
	if err != nil {
		return Release{}, err
	}
	base := trimRegistry(source.RegistryURL)
	query := url.Values{"service": {parsed.Host}, "scope": {"repository:" + source.Repository + ":pull"}}
	data, _, err := fetch(ctx, client, base+"/token?"+query.Encode(), "", 1<<20)
	if err != nil {
		return Release{}, err
	}
	var token struct {
		Token string `json:"token"`
	}
	if err := json.Unmarshal(data, &token); err != nil || token.Token == "" {
		return Release{}, errors.New("Registry bearer token is missing")
	}

	endpoint := base + "/v2/" + source.Repository + "/tags/list?n=100"
	seenPages := map[string]bool{}
	tags := []string{}
	for endpoint != "" {
		if seenPages[endpoint] || len(seenPages) >= 200 {
			return Release{}, errors.New("Registry pagination exceeds bounds or repeats")
		}
		seenPages[endpoint] = true
		data, headers, err := fetch(ctx, client, endpoint, token.Token, maxMetadataBytes)
		if err != nil {
			return Release{}, err
		}
		var page struct {
			Name string   `json:"name"`
			Tags []string `json:"tags"`
		}
		if err := json.Unmarshal(data, &page); err != nil || page.Name != source.Repository {
			return Release{}, errors.New("Registry tag repository mismatch or invalid response")
		}
		tags = append(tags, page.Tags...)
		if len(tags) > maxTagCount {
			return Release{}, errors.New("Registry tag count exceeds bounds")
		}
		endpoint, err = nextTagsPage(endpoint, headers.Get("Link"))
		if err != nil {
			return Release{}, err
		}
	}
	candidates := []string{}
	for _, tag := range tags {
		// '+' is not a legal OCI tag character even though SemVer permits build metadata.
		if validVersion(tag) && !strings.Contains(tag, "+") && !strings.Contains(tag, "-market.") {
			candidates = append(candidates, tag)
		}
	}
	if len(candidates) == 0 {
		return Release{}, errors.New("Registry has no canonical SemVer tags")
	}
	sort.Slice(candidates, func(i, j int) bool { return compareVersions(candidates[i], candidates[j]) > 0 })
	version := candidates[0]
	data, _, err = fetch(ctx, client, base+"/v2/"+source.Repository+"/manifests/"+version, token.Token, 2<<20)
	if err != nil {
		return Release{}, err
	}
	var index struct {
		SchemaVersion int    `json:"schemaVersion"`
		MediaType     string `json:"mediaType"`
		Manifests     []struct {
			Digest   string `json:"digest"`
			Platform struct {
				OS           string `json:"os"`
				Architecture string `json:"architecture"`
			} `json:"platform"`
		} `json:"manifests"`
	}
	if err := json.Unmarshal(data, &index); err != nil || index.SchemaVersion != 2 || (index.MediaType != "application/vnd.oci.image.index.v1+json" && index.MediaType != "application/vnd.docker.distribution.manifest.list.v2+json") {
		return Release{}, errors.New("Registry response is not a manifest index")
	}
	artifacts := map[string]string{}
	for _, descriptor := range index.Manifests {
		key := descriptor.Platform.OS + "-" + descriptor.Platform.Architecture
		if !contains(source.Platforms, key) {
			continue
		}
		if !digestRE.MatchString(descriptor.Digest) || artifacts[key] != "" {
			return Release{}, errors.New("invalid or duplicate platform digest")
		}
		payload, headers, err := fetch(ctx, client, base+"/v2/"+source.Repository+"/manifests/"+descriptor.Digest, token.Token, 2<<20)
		if err != nil {
			return Release{}, err
		}
		actual := fmt.Sprintf("sha256:%x", sha256.Sum256(payload))
		if actual != descriptor.Digest || (headers.Get("Docker-Content-Digest") != "" && headers.Get("Docker-Content-Digest") != actual) {
			return Release{}, errors.New("platform manifest digest mismatch")
		}
		var manifest struct {
			SchemaVersion int `json:"schemaVersion"`
		}
		if json.Unmarshal(payload, &manifest) != nil || manifest.SchemaVersion != 2 {
			return Release{}, errors.New("platform manifest is invalid")
		}
		artifacts[key] = source.image() + ":" + version + "@" + actual
	}
	if len(artifacts) != len(source.Platforms) {
		return Release{}, errors.New("Registry release is missing a required platform")
	}
	return Release{Source: source, Version: version, Artifacts: artifacts}, nil
}

func nextTagsPage(endpoint, link string) (string, error) {
	if link == "" {
		return "", nil
	}
	match := regexp.MustCompile(`^<([^>]+)>;\s*rel="next"$`).FindStringSubmatch(link)
	if match == nil {
		return "", errors.New("unsupported Registry pagination link")
	}
	current, _ := url.Parse(endpoint)
	relative, err := url.Parse(match[1])
	if err != nil {
		return "", err
	}
	next := current.ResolveReference(relative)
	if next.Scheme != "https" || next.Host != current.Host || next.Path != current.Path || next.User != nil || next.Fragment != "" {
		return "", errors.New("unsafe Registry pagination link")
	}
	return next.String(), nil
}

func fetch(ctx context.Context, client *http.Client, endpoint, token string, limit int64) ([]byte, http.Header, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return nil, nil, err
	}
	req.Header.Set("Accept", "application/json, "+manifestAccept)
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	response, err := client.Do(req)
	if err != nil {
		return nil, nil, fmt.Errorf("Registry request failed: %w", err)
	}
	defer response.Body.Close()
	if response.StatusCode != 200 {
		return nil, nil, fmt.Errorf("Registry returned HTTP %d", response.StatusCode)
	}
	data, err := io.ReadAll(io.LimitReader(response.Body, limit+1))
	if err != nil {
		return nil, nil, err
	}
	if int64(len(data)) > limit {
		return nil, nil, errors.New("Registry response exceeds metadata bounds")
	}
	return data, response.Header, nil
}
func contains(values []string, value string) bool {
	for _, item := range values {
		if item == value {
			return true
		}
	}
	return false
}
