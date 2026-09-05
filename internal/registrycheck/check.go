package registrycheck

import (
	"bytes"
	"context"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"
)

const (
	CodeNotFound           = "REGISTRY_NOT_FOUND"
	CodeAuthDenied         = "REGISTRY_AUTH_DENIED"
	CodeRateLimited        = "REGISTRY_RATE_LIMITED"
	CodeTimeout            = "REGISTRY_TIMEOUT"
	CodeNetworkUnavailable = "REGISTRY_NETWORK_UNAVAILABLE"
	CodeResponseInvalid    = "REGISTRY_RESPONSE_INVALID"
	CodeUnavailable        = "REGISTRY_UNAVAILABLE"
	maxTokenResponseBytes  = 1 << 20
)

var ErrNoTemplates = errors.New("catalog contains no OCI templates")

// Error identifies a release preflight failure without exposing response bodies
// or authorization details in command output.
type Error struct {
	Code       string
	TemplateID string
	Detail     string
}

func (e *Error) Error() string {
	if e.TemplateID == "" {
		return fmt.Sprintf("%s: %s", e.Code, e.Detail)
	}
	return fmt.Sprintf("%s (%s): %s", e.Code, e.TemplateID, e.Detail)
}

// Options controls the network boundary. RegistryURL is useful for tests and
// private mirrors; production callers should leave it nil for HTTPS.
type Options struct {
	Client      *http.Client
	RegistryURL func(host string) string
}

type templateDefinition struct {
	SchemaVersion      int               `json:"schema_version"`
	TemplateID         string            `json:"template_id"`
	Deployment         string            `json:"deployment"`
	RecommendedVersion string            `json:"recommended_version"`
	PlatformArtifacts  map[string]string `json:"platform_artifacts"`
	ReleaseDiscovery   *struct {
		Source string `json:"source"`
	} `json:"release_discovery"`
}

type artifact struct {
	host   string
	repo   string
	tag    string
	digest string
}

type manifestIndex struct {
	SchemaVersion int `json:"schemaVersion"`
	Manifests     []struct {
		MediaType string `json:"mediaType"`
		Digest    string `json:"digest"`
		Platform  *struct {
			OS           string `json:"os"`
			Architecture string `json:"architecture"`
		} `json:"platform"`
	} `json:"manifests"`
}

type checkContext struct {
	client      *http.Client
	registryURL func(string) string
}

// Check scans every OCI template under root and verifies its recommended
// release against the source Registry. Non-OCI and discovery-less templates
// are intentionally skipped.
func Check(ctx context.Context, root string, options Options) error {
	entries, err := os.ReadDir(filepath.Join(root, "templates"))
	if err != nil {
		return fmt.Errorf("read templates: %w", err)
	}
	ctxCheck := checkContext{client: options.Client, registryURL: options.RegistryURL}
	if ctxCheck.client == nil {
		ctxCheck.client = &http.Client{Timeout: 20 * time.Second}
	}
	if ctxCheck.registryURL == nil {
		ctxCheck.registryURL = func(host string) string { return "https://" + host }
	}
	discovered := 0
	var failures []error
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		path := filepath.Join(root, "templates", entry.Name(), "template.json")
		data, readErr := os.ReadFile(path)
		if readErr != nil {
			return fmt.Errorf("read %s: %w", path, readErr)
		}
		var definition templateDefinition
		if err := decodeJSON(data, &definition); err != nil {
			return fmt.Errorf("decode %s: %w", path, err)
		}
		if definition.ReleaseDiscovery == nil || definition.ReleaseDiscovery.Source != "oci" {
			continue
		}
		discovered++
		if err := ctxCheck.checkTemplate(ctx, definition); err != nil {
			if errors.Is(err, context.Canceled) {
				return err
			}
			failures = append(failures, err)
			continue
		}
	}
	if discovered == 0 {
		return ErrNoTemplates
	}
	if len(failures) > 0 {
		return errors.Join(failures...)
	}
	return nil
}

func decodeJSON(data []byte, target any) error {
	decoder := json.NewDecoder(bytes.NewReader(data))
	if err := decoder.Decode(target); err != nil {
		return err
	}
	if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		if err == nil {
			return errors.New("trailing JSON content")
		}
		return err
	}
	return nil
}

func (c checkContext) checkTemplate(ctx context.Context, definition templateDefinition) error {
	if definition.SchemaVersion != 2 || definition.TemplateID == "" || definition.RecommendedVersion == "" || definition.Deployment != "container" {
		return c.invalid(definition.TemplateID, "template identity or deployment is invalid")
	}
	if len(definition.PlatformArtifacts) == 0 {
		return c.invalid(definition.TemplateID, "platform_artifacts is empty")
	}
	const requiredPlatforms = 2
	if len(definition.PlatformArtifacts) < requiredPlatforms {
		return c.invalid(definition.TemplateID, "linux/amd64 and linux/arm64 artifacts are required")
	}
	parsed := make(map[string]artifact, len(definition.PlatformArtifacts))
	var source artifact
	for platform, raw := range definition.PlatformArtifacts {
		item, err := parseArtifact(raw)
		if err != nil {
			return c.invalid(definition.TemplateID, fmt.Sprintf("%s artifact: %v", platform, err))
		}
		if item.tag != definition.RecommendedVersion {
			return c.invalid(definition.TemplateID, fmt.Sprintf("%s tag %q does not match recommended version %q", platform, item.tag, definition.RecommendedVersion))
		}
		if source.host == "" {
			source = item
		} else if item.host != source.host || item.repo != source.repo || item.tag != source.tag {
			return c.invalid(definition.TemplateID, "platform artifacts must share one Registry repository and tag")
		}
		parsed[platform] = item
	}
	for _, platform := range []string{"linux-amd64", "linux-arm64"} {
		if _, ok := parsed[platform]; !ok {
			return c.invalid(definition.TemplateID, platform+" artifact is required")
		}
	}
	index, err := c.fetchIndex(ctx, definition.TemplateID, source)
	if err != nil {
		return err
	}
	for platform, expected := range parsed {
		wantOS, wantArch, _ := strings.Cut(platform, "-")
		var descriptorDigest string
		for _, descriptor := range index.Manifests {
			if descriptor.Platform != nil && descriptor.Platform.OS == wantOS && descriptor.Platform.Architecture == wantArch {
				descriptorDigest = descriptor.Digest
				break
			}
		}
		if descriptorDigest == "" {
			return c.invalid(definition.TemplateID, fmt.Sprintf("manifest index has no %s descriptor", platform))
		}
		if descriptorDigest != expected.digest {
			return c.invalid(definition.TemplateID, fmt.Sprintf("%s digest %s does not match declared %s", platform, descriptorDigest, expected.digest))
		}
		if err := c.fetchDigest(ctx, definition.TemplateID, source, descriptorDigest); err != nil {
			return err
		}
	}
	return nil
}

func (c checkContext) fetchIndex(ctx context.Context, templateID string, source artifact) (manifestIndex, error) {
	response, err := c.get(ctx, templateID, source, source.tag)
	if err != nil {
		return manifestIndex{}, err
	}
	defer response.Body.Close()
	var index manifestIndex
	if err := decodeJSONReader(response.Body, &index); err != nil || index.SchemaVersion != 2 || len(index.Manifests) == 0 {
		return manifestIndex{}, c.invalid(templateID, "Registry response is not a manifest index")
	}
	for _, descriptor := range index.Manifests {
		if descriptor.Platform == nil || descriptor.Platform.OS == "" || descriptor.Platform.Architecture == "" || !validDigest(descriptor.Digest) {
			return manifestIndex{}, c.invalid(templateID, "manifest index contains an invalid platform descriptor")
		}
	}
	return index, nil
}

func (c checkContext) fetchDigest(ctx context.Context, templateID string, source artifact, digest string) error {
	response, err := c.get(ctx, templateID, source, digest)
	if err != nil {
		return err
	}
	response.Body.Close()
	return nil
}

func (c checkContext) get(ctx context.Context, templateID string, source artifact, reference string) (*http.Response, error) {
	base := strings.TrimRight(c.registryURL(source.host), "/")
	if parsed, err := url.Parse(base); err != nil || parsed.Scheme == "" || parsed.Host == "" {
		return nil, c.invalid(templateID, "Registry URL is invalid")
	}
	segments := []string{"v2"}
	for _, segment := range strings.Split(source.repo, "/") {
		segments = append(segments, url.PathEscape(segment))
	}
	segments = append(segments, "manifests", url.PathEscape(reference))
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, base+"/"+strings.Join(segments, "/"), nil)
	if err != nil {
		return nil, c.invalid(templateID, "could not construct Registry request")
	}
	request.Header.Set("Accept", "application/vnd.oci.image.index.v1+json, application/vnd.docker.distribution.manifest.list.v2+json, application/vnd.oci.image.manifest.v1+json, application/vnd.docker.distribution.manifest.v2+json")
	response, err := c.client.Do(request)
	if err != nil {
		if errors.Is(err, context.DeadlineExceeded) {
			return nil, &Error{Code: CodeTimeout, TemplateID: templateID, Detail: "Registry request timed out"}
		}
		if errors.Is(err, context.Canceled) {
			return nil, &Error{Code: CodeNetworkUnavailable, TemplateID: templateID, Detail: "Registry request was canceled"}
		}
		return nil, &Error{Code: CodeNetworkUnavailable, TemplateID: templateID, Detail: "Registry network request failed"}
	}
	if response.StatusCode == http.StatusUnauthorized {
		challenge := response.Header.Get("WWW-Authenticate")
		response.Body.Close()
		token, tokenErr := bearerToken(ctx, c.client, challenge, source.repo, templateID)
		if tokenErr != nil {
			return nil, tokenErr
		}
		request.Header.Set("Authorization", "Bearer "+token)
		response, err = c.client.Do(request)
		if err != nil {
			if errors.Is(err, context.DeadlineExceeded) {
				return nil, &Error{Code: CodeTimeout, TemplateID: templateID, Detail: "Registry request timed out"}
			}
			if errors.Is(err, context.Canceled) {
				return nil, &Error{Code: CodeNetworkUnavailable, TemplateID: templateID, Detail: "Registry request was canceled"}
			}
			return nil, &Error{Code: CodeNetworkUnavailable, TemplateID: templateID, Detail: "Registry network request failed"}
		}
	}
	if response.StatusCode == http.StatusNotFound {
		response.Body.Close()
		return nil, &Error{Code: CodeNotFound, TemplateID: templateID, Detail: "recommended tag or platform digest was not found"}
	}
	if response.StatusCode == http.StatusUnauthorized || response.StatusCode == http.StatusForbidden {
		response.Body.Close()
		return nil, &Error{Code: CodeAuthDenied, TemplateID: templateID, Detail: "Registry authentication was rejected"}
	}
	if response.StatusCode == http.StatusTooManyRequests {
		response.Body.Close()
		return nil, &Error{Code: CodeRateLimited, TemplateID: templateID, Detail: "Registry rate limit was reached"}
	}
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		response.Body.Close()
		return nil, &Error{Code: CodeUnavailable, TemplateID: templateID, Detail: fmt.Sprintf("Registry returned HTTP %d", response.StatusCode)}
	}
	return response, nil
}

func bearerToken(ctx context.Context, client *http.Client, challenge, repository, templateID string) (string, error) {
	challenge = strings.TrimSpace(challenge)
	if len(challenge) < len("Bearer ") || !strings.EqualFold(challenge[:len("Bearer ")], "Bearer ") {
		return "", &Error{Code: CodeAuthDenied, TemplateID: templateID, Detail: "Registry authentication was rejected"}
	}
	parameters := map[string]string{}
	for _, part := range strings.Split(challenge[len("Bearer "):], ",") {
		key, value, ok := strings.Cut(strings.TrimSpace(part), "=")
		if ok {
			parameters[strings.ToLower(strings.TrimSpace(key))] = strings.Trim(strings.TrimSpace(value), `"`)
		}
	}
	realm, err := url.Parse(parameters["realm"])
	if err != nil || realm.Scheme != "https" || realm.Hostname() == "" || realm.User != nil {
		return "", &Error{Code: CodeAuthDenied, TemplateID: templateID, Detail: "Registry authentication challenge is invalid"}
	}
	query := realm.Query()
	if service := strings.TrimSpace(parameters["service"]); service != "" {
		query.Set("service", service)
	}
	query.Set("scope", "repository:"+repository+":pull")
	realm.RawQuery = query.Encode()
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, realm.String(), nil)
	if err != nil {
		return "", &Error{Code: CodeAuthDenied, TemplateID: templateID, Detail: "Registry authentication challenge is invalid"}
	}
	request.Header.Set("Accept", "application/json")
	response, err := client.Do(request)
	if err != nil {
		if errors.Is(err, context.DeadlineExceeded) {
			return "", &Error{Code: CodeTimeout, TemplateID: templateID, Detail: "Registry authentication request timed out"}
		}
		if errors.Is(err, context.Canceled) {
			return "", &Error{Code: CodeNetworkUnavailable, TemplateID: templateID, Detail: "Registry authentication request was canceled"}
		}
		return "", &Error{Code: CodeNetworkUnavailable, TemplateID: templateID, Detail: "Registry authentication request failed"}
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		switch response.StatusCode {
		case http.StatusUnauthorized, http.StatusForbidden:
			return "", &Error{Code: CodeAuthDenied, TemplateID: templateID, Detail: "Registry authentication was rejected"}
		case http.StatusTooManyRequests:
			return "", &Error{Code: CodeRateLimited, TemplateID: templateID, Detail: "Registry rate limit was reached"}
		default:
			return "", &Error{Code: CodeUnavailable, TemplateID: templateID, Detail: fmt.Sprintf("Registry authentication returned HTTP %d", response.StatusCode)}
		}
	}
	raw, err := io.ReadAll(io.LimitReader(response.Body, maxTokenResponseBytes+1))
	if err != nil || len(raw) > maxTokenResponseBytes {
		return "", &Error{Code: CodeResponseInvalid, TemplateID: templateID, Detail: "Registry authentication response is invalid"}
	}
	var document struct {
		Token       string `json:"token"`
		AccessToken string `json:"access_token"`
	}
	if err := json.Unmarshal(raw, &document); err != nil {
		return "", &Error{Code: CodeResponseInvalid, TemplateID: templateID, Detail: "Registry authentication response is invalid"}
	}
	token := strings.TrimSpace(document.Token)
	if token == "" {
		token = strings.TrimSpace(document.AccessToken)
	}
	if token == "" {
		return "", &Error{Code: CodeAuthDenied, TemplateID: templateID, Detail: "Registry authentication response did not provide a token"}
	}
	return token, nil
}

func decodeJSONReader(reader io.Reader, target any) error {
	decoder := json.NewDecoder(reader)
	if err := decoder.Decode(target); err != nil {
		return err
	}
	if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		return errors.New("trailing JSON content")
	}
	return nil
}

func parseArtifact(raw string) (artifact, error) {
	value := strings.TrimSpace(raw)
	marker := strings.LastIndex(value, "@sha256:")
	if marker <= 0 {
		return artifact{}, errors.New("artifact must include a sha256 digest")
	}
	digest := "sha256:" + value[marker+len("@sha256:"):]
	if !validDigest(digest) {
		return artifact{}, errors.New("artifact digest is invalid")
	}
	name := value[:marker]
	parts := strings.SplitN(name, "/", 2)
	if len(parts) != 2 || strings.TrimSpace(parts[0]) == "" {
		return artifact{}, errors.New("artifact must include a Registry host, repository, and tag")
	}
	host, path := parts[0], parts[1]
	lastColon := strings.LastIndexByte(path, ':')
	if lastColon <= 0 || lastColon == len(path)-1 {
		return artifact{}, errors.New("artifact repository or tag is invalid")
	}
	repo, tag := path[:lastColon], path[lastColon+1:]
	if strings.TrimSpace(repo) == "" || strings.TrimSpace(tag) == "" {
		return artifact{}, errors.New("artifact repository or tag is invalid")
	}
	return artifact{host: host, repo: repo, tag: tag, digest: digest}, nil
}

func validDigest(value string) bool {
	if !strings.HasPrefix(value, "sha256:") || len(value) != len("sha256:")+64 {
		return false
	}
	_, err := hex.DecodeString(strings.TrimPrefix(value, "sha256:"))
	return err == nil
}

func (c checkContext) invalid(templateID, detail string) error {
	return &Error{Code: CodeResponseInvalid, TemplateID: templateID, Detail: detail}
}
