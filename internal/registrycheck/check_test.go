package registrycheck

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const (
	amd64Digest = "sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	arm64Digest = "sha256:bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"
)

func TestCheckVerifiesManifestIndexPlatformsAndDigests(t *testing.T) {
	root := writeTemplate(t, "linuxserver-webtop", "recommended", amd64Digest, arm64Digest)
	requests := make([]string, 0, 3)
	server := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		requests = append(requests, request.URL.Path)
		switch request.URL.Path {
		case "/v2/linuxserver/webtop/manifests/recommended":
			response.Header().Set("Content-Type", "application/vnd.oci.image.index.v1+json")
			fmt.Fprint(response, `{"schemaVersion":2,"manifests":[{"mediaType":"application/vnd.oci.image.manifest.v1+json","digest":"`+amd64Digest+`","platform":{"os":"linux","architecture":"amd64"}},{"mediaType":"application/vnd.oci.image.manifest.v1+json","digest":"`+arm64Digest+`","platform":{"os":"linux","architecture":"arm64"}}]}`)
		case "/v2/linuxserver/webtop/manifests/" + amd64Digest:
			fmt.Fprint(response, `{ "schemaVersion": 2 }`)
		case "/v2/linuxserver/webtop/manifests/" + arm64Digest:
			fmt.Fprint(response, `{ "schemaVersion": 2 }`)
		default:
			response.WriteHeader(http.StatusNotFound)
		}
	}))
	t.Cleanup(server.Close)

	err := Check(context.Background(), root, Options{RegistryURL: func(string) string { return server.URL }})
	if err != nil {
		t.Fatalf("Check() error = %v", err)
	}
	if len(requests) != 3 {
		t.Fatalf("Registry requests = %v, want tag plus two platform digests", requests)
	}
}

func TestCheckUsesAnonymousBearerChallengeForPublicRegistry(t *testing.T) {
	root := writeTemplate(t, "linuxserver-webtop", "recommended", amd64Digest, arm64Digest)
	var server *httptest.Server
	server = httptest.NewTLSServer(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		if request.URL.Path == "/token" {
			if request.URL.Query().Get("scope") != "repository:linuxserver/webtop:pull" {
				t.Errorf("unexpected token scope %q", request.URL.Query().Get("scope"))
			}
			_ = json.NewEncoder(response).Encode(map[string]string{"token": "preflight-token"})
			return
		}
		if request.Header.Get("Authorization") != "Bearer preflight-token" {
			response.Header().Set("WWW-Authenticate", `Bearer realm="`+server.URL+`/token",service="test"`)
			response.WriteHeader(http.StatusUnauthorized)
			return
		}
		switch request.URL.Path {
		case "/v2/linuxserver/webtop/manifests/recommended":
			_ = json.NewEncoder(response).Encode(map[string]any{"schemaVersion": 2, "manifests": []map[string]any{
				{"digest": amd64Digest, "platform": map[string]string{"os": "linux", "architecture": "amd64"}},
				{"digest": arm64Digest, "platform": map[string]string{"os": "linux", "architecture": "arm64"}},
			}})
		case "/v2/linuxserver/webtop/manifests/" + amd64Digest, "/v2/linuxserver/webtop/manifests/" + arm64Digest:
			_, _ = response.Write([]byte(`{"schemaVersion":2}`))
		default:
			response.WriteHeader(http.StatusNotFound)
		}
	}))
	t.Cleanup(server.Close)
	if err := Check(context.Background(), root, Options{Client: server.Client(), RegistryURL: func(string) string { return server.URL }}); err != nil {
		t.Fatalf("Check() error = %v", err)
	}
}

func TestCheckRejectsPlatformDigestMismatch(t *testing.T) {
	root := writeTemplate(t, "linuxserver-webtop", "recommended", amd64Digest, arm64Digest)
	server := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		if request.URL.Path == "/v2/linuxserver/webtop/manifests/recommended" {
			fmt.Fprint(response, `{"schemaVersion":2,"manifests":[{"digest":"`+amd64Digest+`","platform":{"os":"linux","architecture":"amd64"}},{"digest":"sha256:cccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccc","platform":{"os":"linux","architecture":"arm64"}}]}`)
			return
		}
		fmt.Fprint(response, `{ "schemaVersion": 2 }`)
	}))
	t.Cleanup(server.Close)

	err := Check(context.Background(), root, Options{RegistryURL: func(string) string { return server.URL }})
	assertCode(t, err, CodeResponseInvalid)
}

func TestCheckReportsStableRegistryFailureCodes(t *testing.T) {
	tests := []struct {
		name     string
		status   int
		wantCode string
		body     string
	}{
		{name: "not found", status: http.StatusNotFound, wantCode: CodeNotFound},
		{name: "auth", status: http.StatusUnauthorized, wantCode: CodeAuthDenied},
		{name: "rate limited", status: http.StatusTooManyRequests, wantCode: CodeRateLimited},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			root := writeTemplate(t, "linuxserver-webtop", "recommended", amd64Digest, arm64Digest)
			server := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, _ *http.Request) {
				response.WriteHeader(test.status)
				fmt.Fprint(response, test.body)
			}))
			t.Cleanup(server.Close)
			err := Check(context.Background(), root, Options{RegistryURL: func(string) string { return server.URL }})
			assertCode(t, err, test.wantCode)
		})
	}
}

func TestCheckReportsMalformedManifestIndex(t *testing.T) {
	root := writeTemplate(t, "linuxserver-webtop", "recommended", amd64Digest, arm64Digest)
	server := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, _ *http.Request) {
		fmt.Fprint(response, `{"schemaVersion":2,"manifests":[{"digest":"not-a-digest","platform":{"os":"linux","architecture":"amd64"}}]}`)
	}))
	t.Cleanup(server.Close)
	err := Check(context.Background(), root, Options{RegistryURL: func(string) string { return server.URL }})
	assertCode(t, err, CodeResponseInvalid)
}

func TestCheckReportsNetworkFailure(t *testing.T) {
	root := writeTemplate(t, "linuxserver-webtop", "recommended", amd64Digest, arm64Digest)
	for _, test := range []struct {
		name  string
		cause error
		code  string
	}{
		{name: "connection", cause: errors.New("connection refused"), code: CodeNetworkUnavailable},
		{name: "timeout", cause: context.DeadlineExceeded, code: CodeTimeout},
	} {
		t.Run(test.name, func(t *testing.T) {
			client := &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
				return nil, test.cause
			})}
			err := Check(context.Background(), root, Options{Client: client})
			assertCode(t, err, test.code)
		})
	}
}

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(request *http.Request) (*http.Response, error) { return f(request) }

func writeTemplate(t *testing.T, templateID, tag, amd64, arm64 string) string {
	t.Helper()
	root := t.TempDir()
	dir := filepath.Join(root, "templates", templateID)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	definition := map[string]any{
		"schema_version":      2,
		"template_id":         templateID,
		"deployment":          "container",
		"recommended_version": tag,
		"platform_artifacts": map[string]string{
			"linux-amd64": "lscr.io/linuxserver/webtop:" + tag + "@" + amd64,
			"linux-arm64": "lscr.io/linuxserver/webtop:" + tag + "@" + arm64,
		},
		"release_discovery": map[string]string{"source": "oci"},
	}
	data, err := json.Marshal(definition)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "template.json"), data, 0o644); err != nil {
		t.Fatal(err)
	}
	return root
}

func assertCode(t *testing.T, err error, want string) {
	t.Helper()
	if err == nil {
		t.Fatalf("error = nil, want %s", want)
	}
	var checkErr *Error
	if !errors.As(err, &checkErr) {
		t.Fatalf("error = %T %v, want *Error", err, err)
	}
	if checkErr.Code != want {
		t.Fatalf("error code = %s, want %s (error: %v)", checkErr.Code, want, err)
	}
	if strings.TrimSpace(checkErr.Detail) == "" {
		t.Fatal("error detail is empty")
	}
}
