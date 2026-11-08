package template

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"testing"
)

func TestMarketProjectionAndExpectation(t *testing.T) {
	s := Snapshot{Source: ResolvedSource{Repository: "example/templates", RepositoryID: 123, Ref: "main", CommitSHA: strings.Repeat("a", 40)}, Files: exampleFiles()}
	s.SHA256, _ = Digest(s.Files)
	item, err := InspectMarketTemplate(s)
	if err != nil {
		t.Fatal(err)
	}
	if item.TemplateID != SourceIdentity(123, item.DocumentTemplateID) || item.SHA256 != s.SHA256 || item.Revision < 1 || len(item.Localizations) == 0 {
		t.Fatalf("incomplete projection: %+v", item)
	}
	raw, _ := json.Marshal(item)
	if strings.Contains(string(raw), "start_script") {
		t.Fatal("executable projection")
	}
	expected := SourceExpectation{123, s.Source.CommitSHA, s.SHA256}
	if err := expected.Verify(s); err != nil {
		t.Fatal(err)
	}
	for _, mutate := range []func(*Snapshot){func(s *Snapshot) { s.Source.RepositoryID++ }, func(s *Snapshot) { s.Source.CommitSHA = strings.Repeat("b", 40) }, func(s *Snapshot) { s.Files[0].Content = []byte("tampered") }} {
		raw, _ := json.Marshal(s)
		var changed Snapshot
		json.Unmarshal(raw, &changed)
		mutate(&changed)
		if expected.Verify(changed) == nil {
			t.Fatal("accepted changed evidence")
		}
	}
}
func TestPinnedCaptureNeverResolvesTrackingBranch(t *testing.T) {
	commit := strings.Repeat("a", 40)
	client := GitHubClient{Client: &http.Client{Transport: githubTransport(func(r *http.Request) (*http.Response, error) {
		var result any
		switch r.URL.Path {
		case "/repos/example/templates":
			result = map[string]any{"id": 123, "full_name": "example/templates", "default_branch": "main"}
		case "/repos/example/templates/commits/" + commit:
			result = map[string]any{"sha": strings.Repeat("b", 40), "commit": map[string]any{"tree": map[string]any{"sha": commit}}}
		case "/repos/example/templates/git/trees/" + commit:
			result = map[string]any{"sha": commit, "tree": []any{}}
		default:
			t.Fatalf("unexpected request %s", r.URL.Path)
		}
		raw, _ := json.Marshal(result)
		return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(string(raw))), Header: make(http.Header)}, nil
	})}}
	_, err := client.CapturePinned(context.Background(), GitSource{Repository: "example/templates", Ref: "main"}, SourceExpectation{123, commit, strings.Repeat("c", 64)}, "")
	if err == nil {
		t.Fatal("accepted changed commit")
	}
}
func TestSourceIdentityStable(t *testing.T) {
	if SourceIdentity(123, "web") != "git_ea51909942a76ea23f416e5e664c6eee7671ff7ac8417ce3" {
		t.Fatal(SourceIdentity(123, "web"))
	}
}
