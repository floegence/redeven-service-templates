package template

import (
	"context"
	"crypto/sha1"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"testing"
)

type githubTransport func(*http.Request) (*http.Response, error)

func (f githubTransport) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func TestGitHubCapturePinsCommitAndPreservesPrivateSource(t *testing.T) {
	const commit = "0123456789012345678901234567890123456789"
	files := exampleFiles()
	blobs := map[string]File{}
	tree := []map[string]any{}
	for _, file := range files {
		sum := sha1.Sum(append([]byte(fmt.Sprintf("blob %d\x00", len(file.Content))), file.Content...))
		sha := fmt.Sprintf("%x", sum)
		blobs[sha] = file
		tree = append(tree, map[string]any{"path": file.Path, "mode": file.Mode, "type": "blob", "sha": sha, "size": len(file.Content)})
	}
	requests := 0
	client := &GitHubClient{Client: &http.Client{Transport: githubTransport(func(req *http.Request) (*http.Response, error) {
		requests++
		if req.URL.Host != "api.github.com" || req.Header.Get("Authorization") != "Bearer temporary-secret" {
			t.Fatal("credential or origin boundary changed")
		}
		var result any
		switch {
		case req.URL.Path == "/repos/example/templates":
			result = map[string]any{"id": 123, "full_name": "example/templates", "default_branch": "develop"}
		case req.URL.Path == "/repos/example/templates/commits/develop":
			result = map[string]any{"sha": commit, "commit": map[string]any{"tree": map[string]any{"sha": commit}}}
		case req.URL.Path == "/repos/example/templates/git/trees/"+commit:
			result = map[string]any{"sha": commit, "tree": tree, "truncated": false}
		case strings.HasPrefix(req.URL.Path, "/repos/example/templates/git/blobs/"):
			sha := strings.TrimPrefix(req.URL.Path, "/repos/example/templates/git/blobs/")
			file, ok := blobs[sha]
			if !ok {
				t.Fatal("unknown blob")
			}
			result = map[string]any{"sha": sha, "size": len(file.Content), "encoding": "base64", "content": base64.StdEncoding.EncodeToString(file.Content)}
		default:
			t.Fatalf("unplanned GitHub request: %s", req.URL.Path)
		}
		raw, _ := json.Marshal(result)
		return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(string(raw))), Header: make(http.Header)}, nil
	})}}
	t.Setenv("PATH", t.TempDir())
	snapshot, err := client.Capture(context.Background(), GitSource{Repository: "https://github.com/example/templates"}, "temporary-secret")
	if err != nil {
		t.Fatal(err)
	}
	if snapshot.Source.Ref != "develop" || snapshot.Source.CommitSHA != commit || snapshot.Source.RepositoryID != 123 {
		t.Fatalf("source was not pinned: %+v", snapshot.Source)
	}
	if err := snapshot.Validate(); err != nil {
		t.Fatal(err)
	}
	want, _ := Digest(files)
	if snapshot.SHA256 != want {
		t.Fatal("Git blob content or executable mode changed")
	}
	raw, _ := json.Marshal(snapshot)
	if strings.Contains(string(raw), "temporary-secret") {
		t.Fatal("credential entered snapshot")
	}
	if requests != 3+len(files) {
		t.Fatalf("unexpected request count: %d", requests)
	}
}

func TestGitHubRejectsInvalidSourcesBeforeNetwork(t *testing.T) {
	client := &GitHubClient{Client: &http.Client{Transport: githubTransport(func(*http.Request) (*http.Response, error) {
		t.Fatal("invalid source reached network")
		return nil, nil
	})}}
	for _, source := range []GitSource{{Repository: "https://evil.invalid/a/b"}, {Repository: "https://token@github.com/a/b"}, {Repository: "https://github.com/a/b/releases/latest"}, {Repository: "a/b", Path: "../outside"}, {Repository: "a/b", Ref: "bad\nref"}} {
		if _, err := client.Capture(context.Background(), source, "secret"); err == nil {
			t.Fatalf("invalid source accepted: %+v", source)
		}
	}
}

func TestSnapshotRejectsContentAndProvenanceTampering(t *testing.T) {
	snapshot := Snapshot{Source: ResolvedSource{Repository: "example/templates", RepositoryID: 123, Ref: "main", CommitSHA: strings.Repeat("a", 40)}, Files: exampleFiles()}
	snapshot.SHA256, _ = Digest(snapshot.Files)
	if err := snapshot.Validate(); err != nil {
		t.Fatal(err)
	}
	snapshot.Files[2].Content = []byte("changed")
	if err := snapshot.Validate(); err == nil {
		t.Fatal("modified snapshot accepted")
	}
}
