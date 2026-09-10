package releasewatcher

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/floegence/redeven-service-templates/internal/cataloggen"
)

var integrity = "sha512-" + base64.StdEncoding.EncodeToString(make([]byte, 64))
var platformBody = []byte(`{"schemaVersion":2,"mediaType":"application/vnd.oci.image.manifest.v1+json","config":{},"layers":[]}`)
var platformDigest = fmt.Sprintf("sha256:%x", sha256.Sum256(platformBody))

func testConfig(t *testing.T) Config {
	t.Helper()
	config, err := LoadConfig("../..")
	if err != nil {
		t.Fatal(err)
	}
	return config
}

func npmMetadata(source Source) map[string]any {
	return map[string]any{"name": source.PackageName, "dist-tags": map[string]any{"latest": "0.1.2-rc.1"}, "versions": map[string]any{"0.1.2-rc.1": map[string]any{
		"name": source.PackageName, "version": "0.1.2-rc.1",
		"repository": map[string]any{"type": "git", "url": source.RepositoryURL, "directory": source.RepositoryDirectory},
		"dist":       map[string]any{"integrity": integrity},
	}}}
}

func registryFixture(t *testing.T, mutate func(string, map[string]any), statusPath string, status int) (Config, *http.Client, func()) {
	t.Helper()
	config := testConfig(t)
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		route := r.URL.Path
		if route == statusPath {
			w.WriteHeader(status)
			return
		}
		var payload map[string]any
		switch {
		case route == "/"+config.Sources[0].PackageName:
			payload = npmMetadata(config.Sources[0])
		case route == "/token":
			payload = map[string]any{"token": "fixture"}
		case strings.HasSuffix(route, "/tags/list"):
			if r.Header.Get("Authorization") != "Bearer fixture" {
				t.Error("missing bearer token")
			}
			tags := []string{"0.1.1-rc.2", "0.1.2-rc.1-r1-market.1", "01.0.0", "2.0.0-market.1", "1.0.0+build", "not-a-version"}
			if r.URL.Query().Get("last") == "" {
				w.Header().Set("Link", `<`+route+`?n=100&last=page1>; rel="next"`)
			} else {
				tags = []string{"0.1.2-rc.1-r1"}
			}
			payload = map[string]any{"name": config.Sources[1].Repository, "tags": tags}
		case strings.HasSuffix(route, "/manifests/"+platformDigest):
			w.Header().Set("Docker-Content-Digest", platformDigest)
			_, _ = w.Write(platformBody)
			return
		case strings.Contains(route, "/manifests/"):
			if !strings.HasSuffix(route, "/0.1.2-rc.1-r1") {
				t.Errorf("selected unexpected tag: %s", route)
			}
			descriptors := []any{}
			for _, arch := range []string{"amd64", "arm64"} {
				descriptors = append(descriptors, map[string]any{"digest": platformDigest, "platform": map[string]any{"os": "linux", "architecture": arch}})
			}
			payload = map[string]any{"schemaVersion": 2, "mediaType": "application/vnd.oci.image.index.v1+json", "manifests": descriptors}
		default:
			http.NotFound(w, r)
			return
		}
		if mutate != nil {
			mutate(route, payload)
		}
		_ = json.NewEncoder(w).Encode(payload)
	}))
	for i := range config.Sources {
		config.Sources[i].RegistryURL = server.URL
	}
	return config, server.Client(), server.Close
}

func TestDiscoverPaginatedCanonicalReleases(t *testing.T) {
	config, client, closeServer := registryFixture(t, nil, "", 0)
	defer closeServer()
	plan, err := discover(t.Context(), config, client)
	if err != nil {
		t.Fatal(err)
	}
	if plan.Releases[0].Version != "0.1.2-rc.1" || plan.Releases[1].Version != "0.1.2-rc.1-r1" {
		t.Fatalf("unexpected releases: %+v", plan)
	}
	for _, platform := range config.Sources[1].Platforms {
		want := config.Sources[1].image() + ":0.1.2-rc.1-r1@" + platformDigest
		if plan.Releases[1].Artifacts[platform] != want {
			t.Fatalf("artifact mismatch")
		}
	}
}

func TestRejectUntrustedMetadata(t *testing.T) {
	for _, scenario := range []string{"npm-name", "latest", "deprecated", "repository", "integrity", "version", "oci-name", "platform", "digest", "duplicate", "index"} {
		t.Run(scenario, func(t *testing.T) {
			config, client, closeServer := registryFixture(t, func(route string, p map[string]any) {
				if strings.HasPrefix(route, "/@") {
					v := p["versions"].(map[string]any)["0.1.2-rc.1"].(map[string]any)
					switch scenario {
					case "npm-name":
						p["name"] = "different"
					case "latest":
						p["dist-tags"] = map[string]any{}
					case "deprecated":
						v["deprecated"] = "retired"
					case "repository":
						v["repository"] = map[string]any{"type": "git", "url": "unrelated"}
					case "integrity":
						v["dist"] = map[string]any{}
					case "version":
						v["version"] = "1.0.0"
					}
				}
				if scenario == "oci-name" && strings.HasSuffix(route, "/tags/list") {
					p["name"] = "unrelated"
				}
				if !strings.Contains(route, "/manifests/") {
					return
				}
				descriptors := p["manifests"].([]any)
				switch scenario {
				case "platform":
					p["manifests"] = descriptors[:1]
				case "digest":
					descriptors[0].(map[string]any)["digest"] = "sha256:bad"
				case "duplicate":
					p["manifests"] = append(descriptors, descriptors[0])
				case "index":
					p["mediaType"] = "application/json"
				}
			}, "", 0)
			defer closeServer()
			if _, err := discover(t.Context(), config, client); err == nil {
				t.Fatal("accepted untrusted metadata")
			}
		})
	}
}

func TestDigestBodyMismatch(t *testing.T) {
	config, client, closeServer := registryFixture(t, nil, "", 0)
	defer closeServer()
	original := client.Transport
	client.Transport = roundTripFunc(func(r *http.Request) (*http.Response, error) {
		response, err := original.RoundTrip(r)
		if err == nil && strings.Contains(r.URL.Path, "/manifests/sha256:") {
			response.Header.Set("Docker-Content-Digest", "sha256:"+strings.Repeat("0", 64))
		}
		return response, err
	})
	if _, err := discover(t.Context(), config, client); err == nil || !strings.Contains(err.Error(), "digest mismatch") {
		t.Fatalf("error=%v", err)
	}
}

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func TestNetworkFailures(t *testing.T) {
	config := testConfig(t)
	paths := []string{"/" + config.Sources[0].PackageName, "/token", "/v2/" + config.Sources[1].Repository + "/tags/list", "/v2/" + config.Sources[1].Repository + "/manifests/0.1.2-rc.1-r1"}
	for _, p := range paths {
		for _, status := range []int{401, 403, 404, 429} {
			t.Run(fmt.Sprintf("%s/%d", p, status), func(t *testing.T) {
				config, client, closeServer := registryFixture(t, nil, p, status)
				defer closeServer()
				if _, err := discover(t.Context(), config, client); err == nil || !strings.Contains(err.Error(), fmt.Sprintf("HTTP %d", status)) {
					t.Fatalf("error=%v", err)
				}
			})
		}
	}
	for _, failure := range []error{context.DeadlineExceeded, errors.New("network unavailable")} {
		client := &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) { return nil, failure })}
		if _, err := discover(t.Context(), config, client); !errors.Is(err, failure) {
			t.Fatalf("error=%v", err)
		}
	}
	ctx, cancel := context.WithDeadline(t.Context(), time.Now().Add(-time.Second))
	defer cancel()
	if _, err := discover(ctx, config, &http.Client{}); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("timeout=%v", err)
	}
}

func TestSourceAndPaginationValidation(t *testing.T) {
	for _, url := range []string{"http://ghcr.io", "https://user:password@ghcr.io", "https://ghcr.io/other", "https://ghcr.io?query=1"} {
		config := testConfig(t)
		config.Sources[0].RegistryURL = url
		if config.validate() == nil {
			t.Fatalf("accepted %s", url)
		}
	}
	config := testConfig(t)
	config.Sources = append(config.Sources, config.Sources[0])
	if config.validate() == nil {
		t.Fatal("accepted duplicate")
	}
	for _, link := range []string{`<https://attacker.test/v2/repo/tags/list>; rel="next"`, `<http://ghcr.io/v2/repo/tags/list>; rel="next"`, `</different>; rel="next"`, "malformed"} {
		if _, err := nextTagsPage("https://ghcr.io/v2/repo/tags/list", link); err == nil {
			t.Fatalf("accepted %s", link)
		}
	}
}

func currentPlan(t *testing.T, root string) ReleasePlan {
	t.Helper()
	config, err := LoadConfig(root)
	if err != nil {
		t.Fatal(err)
	}
	plan := ReleasePlan{}
	for _, source := range config.Sources {
		data, err := os.ReadFile(filepath.Join(root, "templates", source.TemplateID, "redeven-service-template.json"))
		if err != nil {
			t.Fatal(err)
		}
		var def cataloggen.TemplateDefinition
		if err := decodeJSON(data, &def); err != nil {
			t.Fatal(err)
		}
		plan.Releases = append(plan.Releases, Release{Source: source, Version: def.RecommendedVersion, Artifacts: def.PlatformArtifacts, Integrity: integrity})
	}
	return plan
}
func fixtureRoot(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	if err := copyTree("../..", root); err != nil {
		t.Fatal(err)
	}
	return root
}
func snapshot(t *testing.T, root string) map[string]string {
	t.Helper()
	result := map[string]string{}
	if err := filepath.WalkDir(root, func(p string, e os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if e.IsDir() {
			return nil
		}
		b, err := os.ReadFile(p)
		if err != nil {
			return err
		}
		relative, _ := filepath.Rel(root, p)
		result[relative] = fmt.Sprintf("%x", sha256.Sum256(b))
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	return result
}
func localValidation(ctx context.Context, root string) error {
	version, _, err := catalogConstants(root)
	if err != nil {
		return err
	}
	output, err := cataloggen.GenerateWithVersion(root, version)
	if err != nil {
		return err
	}
	return cataloggen.Verify(root, output)
}

func TestApplyOnlyChangesAffectedRevisionAndIsDeterministic(t *testing.T) {
	root := fixtureRoot(t)
	initialVersion, _, err := catalogConstants(root)
	if err != nil {
		t.Fatal(err)
	}
	initialParts := strings.Split(initialVersion, ".")
	initialPatch, _ := strconv.Atoi(initialParts[2])
	expectedVersion := fmt.Sprintf("%s.%s.%d", initialParts[0], initialParts[1], initialPatch+1)
	plan := currentPlan(t, root)
	plan.Releases[0].Version = "9.0.0"
	hostPath := filepath.Join(root, "templates", plan.Releases[0].Source.TemplateID, "redeven-service-template.json")
	initialData, _ := os.ReadFile(hostPath)
	var initialHost cataloggen.TemplateDefinition
	if err := decodeJSON(initialData, &initialHost); err != nil {
		t.Fatal(err)
	}
	containerPath := filepath.Join(root, "templates", plan.Releases[1].Source.TemplateID, "redeven-service-template.json")
	beforeContainer, _ := os.ReadFile(containerPath)
	before := snapshot(t, root)
	changed, err := apply(t.Context(), root, plan, localValidation)
	if err != nil || !changed {
		t.Fatalf("changed=%v err=%v", changed, err)
	}
	afterContainer, _ := os.ReadFile(containerPath)
	if string(beforeContainer) != string(afterContainer) {
		t.Fatal("unrelated container revision changed")
	}
	data, _ := os.ReadFile(filepath.Join(root, "templates", plan.Releases[0].Source.TemplateID, "redeven-service-template.json"))
	var host cataloggen.TemplateDefinition
	if err := decodeJSON(data, &host); err != nil {
		t.Fatal(err)
	}
	if host.Revision != initialHost.Revision+1 || host.RecommendedVersion != "9.0.0" {
		t.Fatalf("host=%+v", host)
	}
	version, _, _ := catalogConstants(root)
	if version != expectedVersion {
		t.Fatalf("version=%s", version)
	}
	after := snapshot(t, root)
	changed, err = apply(t.Context(), root, plan, func(context.Context, string) error { t.Fatal("no-op ran validator"); return nil })
	if err != nil || changed || !reflect.DeepEqual(after, snapshot(t, root)) {
		t.Fatalf("no-op changed checkout: %v", err)
	}
	second := fixtureRoot(t)
	if !reflect.DeepEqual(before, snapshot(t, second)) {
		t.Fatal("fixture is not deterministic")
	}
	if _, err := apply(t.Context(), second, plan, localValidation); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(after, snapshot(t, second)) {
		t.Fatal("update bytes are not deterministic")
	}
}

func TestApplyFailureLeavesAllFilesUnchanged(t *testing.T) {
	for _, scenario := range []string{"validation", "constant", "identity", "moved-tag", "integrity", "regression"} {
		t.Run(scenario, func(t *testing.T) {
			root := fixtureRoot(t)
			plan := currentPlan(t, root)
			plan.Releases[0].Version = "9.0.0"
			validate := localValidation
			switch scenario {
			case "validation":
				validate = func(context.Context, string) error { return errors.New("gate failure") }
			case "constant":
				initialVersion, _, _ := catalogConstants(root)
				p := filepath.Join(root, "catalog.go")
				data, _ := os.ReadFile(p)
				_ = os.WriteFile(p, []byte(strings.ReplaceAll(string(data), initialVersion, "v0.0.0")), 0644)
			case "identity":
				plan.Releases[0].Source.TemplateID = "unrelated"
			case "moved-tag":
				plan.Releases[1].Artifacts["linux-amd64"] = strings.Split(plan.Releases[1].Artifacts["linux-amd64"], "@")[0] + "@sha256:" + strings.Repeat("0", 64)
			case "integrity":
				plan.Releases[0].Integrity = ""
			case "regression":
				plan.Releases[0].Version = "0.0.1"
			}
			before := snapshot(t, root)
			if _, err := apply(t.Context(), root, plan, validate); err == nil {
				t.Fatal("expected error")
			}
			if !reflect.DeepEqual(before, snapshot(t, root)) {
				t.Fatal("failed update changed files")
			}
		})
	}
}

func TestReplaceRollback(t *testing.T) {
	root := t.TempDir()
	originals := map[string][]byte{"a": []byte("a"), "b": []byte("b")}
	for p, b := range originals {
		_ = os.WriteFile(filepath.Join(root, p), b, 0644)
	}
	count := 0
	err := replaceFiles(root, originals, map[string][]byte{"a": []byte("new"), "b": []byte("new")}, func(a, b string) error {
		count++
		if count == 2 {
			return errors.New("disk error")
		}
		return os.Rename(a, b)
	})
	if err == nil {
		t.Fatal("expected error")
	}
	for p, b := range originals {
		actual, _ := os.ReadFile(filepath.Join(root, p))
		if string(actual) != string(b) {
			t.Fatal("rollback failed")
		}
	}
}

func TestSemVer(t *testing.T) {
	ordered := []string{"1.0.0-alpha", "1.0.0-alpha.1", "1.0.0-alpha.beta", "1.0.0-beta", "1.0.0-beta.2", "1.0.0-beta.11", "1.0.0-rc.1", "1.0.0"}
	for i, v := range ordered {
		if !validVersion(v) {
			t.Fatal(v)
		}
		if i > 0 && compareVersions(ordered[i-1], v) >= 0 {
			t.Fatal("ordering")
		}
	}
	if compareVersions("1.0.0-99999999999999999999999", "1.0.0-x") >= 0 {
		t.Fatal("numeric overflow")
	}
	for _, v := range []string{"1.0.0-01", "01.0.0", "v1.0.0", "1.0.0 ", "1.0"} {
		if validVersion(v) {
			t.Fatal(v)
		}
	}
}
