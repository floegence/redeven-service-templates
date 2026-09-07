package releasewatcher

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

func TestDiscoverAndApply(t *testing.T) {
	const amd64 = "sha256:9f2de8bfecfa87f82d0fdfc8a7edbc88e59046b2a11c9817ff9e1e826da886e0"
	const arm64 = "sha256:386c8bb5578d4976b16056041073613b355ac7dc46203f80fb51c99e5aeaa907"
	server := httptest.NewTLSServer(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		response.Header().Set("Content-Type", "application/json")
		switch {
		case request.URL.Path == "/@deepseek-ai/dsh":
			_, _ = response.Write([]byte(`{"name":"@deepseek-ai/dsh","dist-tags":{"latest":"0.1.2-rc.1"},"versions":{"0.1.2-rc.1":{"version":"0.1.2-rc.1","repository":{"type":"git","url":"git+https://github.com/deepseek-ai/deepseek-harness.git","directory":"apps/cli"},"dist":{"integrity":"sha512-RPq48TzxvwpdT9/7W1tbhZDBMmeK+bxDrX9cqQC27Wx/LqtgJF8PSa3b3xriU8oxtvhwYmk21w2cej3uMQrnVA=="}}}}`))
		case request.URL.Path == "/token":
			_, _ = response.Write([]byte(`{"token":"test-token"}`))
		case request.URL.Path == "/v2/runzhliu/deepseek-harness/tags/list":
			_, _ = response.Write([]byte(`{"name":"runzhliu/deepseek-harness","tags":["0.1.1-rc.2","0.1.2-rc.1-r1","0.1.2-rc.1-r1-market.1","not-a-version"]}`))
		case strings.HasPrefix(request.URL.Path, "/v2/runzhliu/deepseek-harness/manifests/"):
			_, _ = response.Write([]byte(`{"schemaVersion":2,"manifests":[{"digest":"` + amd64 + `","platform":{"os":"linux","architecture":"amd64"}},{"digest":"` + arm64 + `","platform":{"os":"linux","architecture":"arm64"}}]}`))
		default:
			http.NotFound(response, request)
		}
	}))
	defer server.Close()

	sources := Sources{
		NPMPackage: DefaultNPMPackage, NPMRegistry: server.URL,
		RepositoryURL: DefaultRepositoryURL, RepositoryDir: DefaultRepositoryDir,
		GHCRRegistry: server.URL, GHCRRepository: DefaultGHCRRepository,
	}
	plan, err := Discover(t.Context(), Options{Client: server.Client(), Sources: sources})
	if err != nil {
		t.Fatal(err)
	}
	if plan.HostVersion != "0.1.2-rc.1" || plan.ContainerVersion != "0.1.2-rc.1-r1" {
		t.Fatalf("unexpected plan: %+v", plan)
	}
	if plan.ContainerArtifacts["linux-amd64"] != "ghcr.io/runzhliu/deepseek-harness:0.1.2-rc.1-r1@"+amd64 {
		t.Fatalf("unexpected amd64 artifact: %s", plan.ContainerArtifacts["linux-amd64"])
	}

	root := t.TempDir()
	if err := copyTree(filepath.Join("..", ".."), root); err != nil {
		t.Fatal(err)
	}
	downgradeCatalogForTest(t, root)
	changed, err := Apply(root, plan)
	if err != nil {
		t.Fatal(err)
	}
	if !changed {
		t.Fatal("Apply reported no change for an older catalog")
	}
	beforeNoOp := make(map[string][]byte)
	for _, relative := range []string{
		"templates/deepseek-harness-host/template.json",
		"templates/deepseek-harness-container/template.json",
		"catalog.go", "internal/cataloggen/generate.go",
		"dist/catalog.bundle.json", "dist/catalog.bundle.manifest.json", "dist/catalog.bundle.sha256",
	} {
		data, err := os.ReadFile(filepath.Join(root, relative))
		if err != nil {
			t.Fatal(err)
		}
		beforeNoOp[relative] = data
	}
	changed, err = Apply(root, plan)
	if err != nil {
		t.Fatal(err)
	}
	if changed {
		t.Fatal("Apply changed an already current catalog")
	}
	for relative, before := range beforeNoOp {
		after, err := os.ReadFile(filepath.Join(root, relative))
		if err != nil {
			t.Fatal(err)
		}
		if string(after) != string(before) {
			t.Fatalf("no-op changed %s", relative)
		}
	}

	var host map[string]any
	readJSONFile(t, filepath.Join(root, "templates/deepseek-harness-host/template.json"), &host)
	if host["recommended_version"] != "0.1.2-rc.1" || host["revision"] != float64(8) {
		t.Fatalf("host update = %+v", host)
	}
	var container map[string]any
	readJSONFile(t, filepath.Join(root, "templates/deepseek-harness-container/template.json"), &container)
	if container["recommended_version"] != "0.1.2-rc.1-r1" || container["revision"] != float64(5) {
		t.Fatalf("container update = %+v", container)
	}
	version, err := readCatalogVersion(root)
	if err != nil || version != "v0.4.2" {
		t.Fatalf("catalog version = %q, err=%v", version, err)
	}
}

func TestDiscoverRejectsMissingIntegrity(t *testing.T) {
	server := httptest.NewTLSServer(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		if request.URL.Path == "/@deepseek-ai/dsh" {
			_, _ = response.Write([]byte(`{"name":"@deepseek-ai/dsh","dist-tags":{"latest":"0.1.2-rc.1"},"versions":{"0.1.2-rc.1":{"version":"0.1.2-rc.1","repository":{"type":"git","url":"git+https://github.com/deepseek-ai/deepseek-harness.git","directory":"apps/cli"},"dist":{}}}}`))
			return
		}
		http.NotFound(response, request)
	}))
	defer server.Close()
	sources := DefaultSources()
	sources.NPMRegistry = server.URL
	if _, err := discoverNPM(t.Context(), server.Client(), sources); err == nil || !strings.Contains(err.Error(), "SHA-512 integrity") {
		t.Fatalf("discoverNPM error = %v", err)
	}
}

func TestDiscoverNPMRejectsUntrustedMetadata(t *testing.T) {
	valid := `{"name":"@deepseek-ai/dsh","dist-tags":{"latest":"0.1.2-rc.1"},"versions":{"0.1.2-rc.1":{"version":"0.1.2-rc.1","repository":{"type":"git","url":"git+https://github.com/deepseek-ai/deepseek-harness.git","directory":"apps/cli"},"dist":{"integrity":"sha512-RPq48TzxvwpdT9/7W1tbhZDBMmeK+bxDrX9cqQC27Wx/LqtgJF8PSa3b3xriU8oxtvhwYmk21w2cej3uMQrnVA=="}}}}`
	tests := []struct {
		name    string
		mutate  func(string) string
		message string
	}{
		{"missing latest", func(string) string { return `{"name":"@deepseek-ai/dsh","dist-tags":{},"versions":{}}` }, "latest dist-tag"},
		{"deprecated", func(value string) string {
			return strings.Replace(value, `"dist":{"integrity"`, `"deprecated":"use another package","dist":{"integrity"`, 1)
		}, "deprecated"},
		{"wrong repository", func(value string) string {
			return strings.Replace(value, "deepseek-ai/deepseek-harness.git", "someone/other.git", 1)
		}, "repository"},
		{"missing integrity", func(value string) string {
			return strings.Replace(value, `"integrity":"sha512-RPq48TzxvwpdT9/7W1tbhZDBMmeK+bxDrX9cqQC27Wx/LqtgJF8PSa3b3xriU8oxtvhwYmk21w2cej3uMQrnVA=="`, "", 1)
		}, "SHA-512 integrity"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			server := httptest.NewTLSServer(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
				_, _ = response.Write([]byte(test.mutate(valid)))
			}))
			defer server.Close()
			sources := DefaultSources()
			sources.NPMRegistry = server.URL
			_, err := discoverNPM(t.Context(), server.Client(), sources)
			if err == nil || !strings.Contains(err.Error(), test.message) {
				t.Fatalf("discoverNPM error = %v, want %q", err, test.message)
			}
		})
	}
}

func TestDiscoverOCIRejectsMissingPlatform(t *testing.T) {
	server := httptest.NewTLSServer(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		switch {
		case request.URL.Path == "/token":
			_, _ = response.Write([]byte(`{"token":"token"}`))
		case request.URL.Path == "/v2/runzhliu/deepseek-harness/tags/list":
			_, _ = response.Write([]byte(`{"tags":["0.1.2-rc.1-r1","0.1.2-rc.1-r1-market.1","0.1.2.99"]}`))
		default:
			_, _ = response.Write([]byte(`{"schemaVersion":2,"manifests":[{"digest":"sha256:9f2de8bfecfa87f82d0fdfc8a7edbc88e59046b2a11c9817ff9e1e826da886e0","platform":{"os":"linux","architecture":"amd64"}}]}`))
		}
	}))
	defer server.Close()
	sources := DefaultSources()
	sources.GHCRRegistry = server.URL
	if _, err := discoverOCI(t.Context(), server.Client(), sources); err == nil || !strings.Contains(err.Error(), "amd64 and arm64") {
		t.Fatalf("discoverOCI error = %v", err)
	}
}

func TestDiscoverOCIRejectsInsecureRegistry(t *testing.T) {
	sources := DefaultSources()
	sources.GHCRRegistry = "http://ghcr.example.invalid"
	if _, err := discoverOCI(t.Context(), http.DefaultClient, sources); err == nil || !strings.Contains(err.Error(), "absolute HTTPS") {
		t.Fatalf("discoverOCI error = %v", err)
	}
}

func TestSemVerCanonicalSelection(t *testing.T) {
	values := []struct {
		value string
		valid bool
	}{
		{"0.1.2-rc.1-r1", true}, {"1.0.0", true}, {"1.0.0-market.1", true},
		{"01.0.0", false}, {"1.0", false}, {"1.0.0-01", false},
	}
	for _, value := range values {
		if got := validVersion(value.value); got != value.valid {
			t.Errorf("validVersion(%q) = %v, want %v", value.value, got, value.valid)
		}
	}
	if compareVersions("1.0.0", "1.0.0-rc.9") <= 0 || compareVersions("1.0.0-rc.10", "1.0.0-rc.2") <= 0 {
		t.Fatal("semver precedence is incorrect")
	}
}

func TestFetchFailsClosedForRegistryStatusesAndTransport(t *testing.T) {
	for _, status := range []int{401, 403, 404, 429} {
		t.Run(strconv.Itoa(status), func(t *testing.T) {
			server := httptest.NewTLSServer(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) { response.WriteHeader(status) }))
			defer server.Close()
			_, err := fetchJSON(t.Context(), server.Client(), server.URL, "registry")
			if err == nil || !strings.Contains(err.Error(), "HTTP "+strconv.Itoa(status)) {
				t.Fatalf("fetch error = %v", err)
			}
		})
	}
	transportError := errors.New("network unavailable")
	client := &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) { return nil, transportError })}
	if _, err := fetchJSON(context.Background(), client, "https://registry.invalid", "registry"); err == nil || !strings.Contains(err.Error(), "request failed") {
		t.Fatalf("transport error = %v", err)
	}
}

type roundTripFunc func(*http.Request) (*http.Response, error)

func (function roundTripFunc) RoundTrip(request *http.Request) (*http.Response, error) {
	return function(request)
}

func downgradeCatalogForTest(t *testing.T, root string) {
	t.Helper()
	for relative, version := range map[string]string{
		"templates/deepseek-harness-host/template.json":      "0.1.1-rc.2",
		"templates/deepseek-harness-container/template.json": "0.1.1-rc.2",
	} {
		path := filepath.Join(root, relative)
		data, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		text := string(data)
		if strings.Contains(relative, "container") {
			text = strings.ReplaceAll(text, "0.1.2-rc.1-r1", version)
		} else {
			text = strings.ReplaceAll(text, "0.1.2-rc.1", version)
		}
		data = []byte(text)
		if err := os.WriteFile(path, data, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	for _, relative := range []string{"catalog.go", "internal/cataloggen/generate.go"} {
		path := filepath.Join(root, relative)
		data, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		data = []byte(strings.ReplaceAll(string(data), "v0.4.2", "v0.4.1"))
		if err := os.WriteFile(path, data, 0o644); err != nil {
			t.Fatal(err)
		}
	}
}

func readJSONFile(t *testing.T, path string, destination any) {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(data, destination); err != nil {
		t.Fatal(err)
	}
}
