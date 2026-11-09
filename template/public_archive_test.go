package template

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func publicArchiveFixture(t *testing.T, source GitSource, commit string, files []File) []byte {
	t.Helper()
	var raw bytes.Buffer
	gz := gzip.NewWriter(&raw)
	tw := tar.NewWriter(gz)
	if err := tw.WriteHeader(&tar.Header{Typeflag: tar.TypeXGlobalHeader, Name: "pax_global_header", PAXRecords: map[string]string{"comment": commit}}); err != nil {
		t.Fatal(err)
	}
	root := "templates-" + commit
	if err := tw.WriteHeader(&tar.Header{Typeflag: tar.TypeDir, Name: root + "/", Mode: 0775}); err != nil {
		t.Fatal(err)
	}
	for _, file := range files {
		mode := int64(0664)
		if file.Mode == "100755" {
			mode = 0775
		}
		if err := tw.WriteHeader(&tar.Header{Name: root + "/" + source.Path + "/" + file.Path, Mode: mode, Size: int64(len(file.Content))}); err != nil {
			t.Fatal(err)
		}
		tw.Write(file.Content)
	}
	tw.Close()
	gz.Close()
	return raw.Bytes()
}
func TestPublicPinnedCaptureWithoutAPIQuota(t *testing.T) {
	source := GitSource{Repository: "owner/templates", Ref: "main", Path: "templates/app"}
	commit := strings.Repeat("a", 40)
	files := []File{{Path: "template.json", Mode: "100644", Content: []byte("{}")}, {Path: "start.sh", Mode: "100755", Content: []byte("exec server\n")}}
	digest, _ := Digest(files)
	expected := SourceExpectation{RepositoryID: 123, CommitSHA: commit, SHA256: digest}
	calls := 0
	c := GitHubClient{Client: &http.Client{Transport: githubTransport(func(r *http.Request) (*http.Response, error) {
		calls++
		if r.URL.Host == "api.github.com" {
			return &http.Response{StatusCode: 403, Header: http.Header{"X-Ratelimit-Remaining": []string{"0"}}, Body: io.NopCloser(strings.NewReader("{}"))}, nil
		}
		if r.URL.Host != "codeload.github.com" || r.URL.Path != "/owner/templates/tar.gz/"+commit || r.Header.Get("Authorization") != "" {
			t.Fatal("unexpected archive request")
		}
		return &http.Response{StatusCode: 200, Header: make(http.Header), Body: io.NopCloser(bytes.NewReader(publicArchiveFixture(t, source, commit, files)))}, nil
	})}}
	snapshot, err := c.CapturePinned(context.Background(), source, expected, "")
	if err != nil {
		t.Fatal(err)
	}
	if calls != 1 || snapshot.SHA256 != digest || snapshot.Source.Ref != "main" || len(snapshot.Files) != 2 {
		t.Fatal("pinned source mismatch")
	}
}

func TestPublicArchiveRejectsInvalidBytes(t *testing.T) {
	source := GitSource{Repository: "owner/templates", Ref: "main", Path: "templates/app"}
	commit := strings.Repeat("a", 40)
	files := []File{{Path: "template.json", Mode: "100644", Content: []byte("{}")}}
	digest, _ := Digest(files)
	expected := SourceExpectation{RepositoryID: 123, CommitSHA: commit, SHA256: digest}
	packed := publicArchiveFixture(t, source, commit, files)
	gz, _ := gzip.NewReader(bytes.NewReader(packed))
	raw, _ := io.ReadAll(gz)
	gz.Close()
	repack := func(data []byte) []byte {
		var out bytes.Buffer
		g := gzip.NewWriter(&out)
		g.Write(data)
		g.Close()
		return out.Bytes()
	}
	corrupt := append([]byte{}, raw...)
	corrupt[0] ^= 1
	for name, data := range map[string][]byte{"bad gzip": []byte("invalid"), "truncated gzip": packed[:len(packed)-8], "checksum": repack(corrupt), "no trailer": repack(raw[:len(raw)-1024]), "trailing data": repack(append(raw, []byte("hidden")...)), "changed bytes": publicArchiveFixture(t, source, commit, []File{{Path: "template.json", Mode: "100644", Content: []byte("changed")}}), "traversal": publicArchiveFixture(t, source, commit, []File{{Path: "../bad", Mode: "100644", Content: []byte("bad")}})} {
		t.Run(name, func(t *testing.T) {
			calls := 0
			c := GitHubClient{Client: &http.Client{Transport: githubTransport(func(r *http.Request) (*http.Response, error) {
				calls++
				return &http.Response{StatusCode: 200, Body: io.NopCloser(bytes.NewReader(data))}, nil
			})}}
			if _, err := c.CapturePinned(context.Background(), source, expected, ""); err == nil {
				t.Fatal("accepted invalid archive")
			}
			if calls != 1 {
				t.Fatal("retried")
			}
		})
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := (&GitHubClient{}).CapturePinned(ctx, source, expected, ""); !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
	for _, status := range []int{404, 429, 500} {
		calls := 0
		c := GitHubClient{Client: &http.Client{Transport: githubTransport(func(r *http.Request) (*http.Response, error) {
			calls++
			return &http.Response{StatusCode: status, Body: io.NopCloser(strings.NewReader(""))}, nil
		})}}
		if _, err := c.CapturePinned(context.Background(), source, expected, ""); err == nil || calls != 1 {
			t.Fatal(status, err, calls)
		}
	}
}

// One generated PAX fixture crosses both published language boundaries. This
// protects UTF-8 paths, long paths, executable modes, ordering and digest parity.
func TestPublicArchiveNodeGoParity(t *testing.T) {
	source := GitSource{Repository: "owner/templates", Ref: "main", Path: "templates/app"}
	commit := strings.Repeat("a", 40)
	files := []File{{Path: "template.json", Mode: "100644", Content: []byte("{}")}, {Path: strings.Repeat("segment/", 15) + "script.sh", Mode: "100755", Content: []byte("exec server\n")}, {Path: "locales/日本語.json", Mode: "100644", Content: []byte("{}")}}
	digest, err := Digest(files)
	if err != nil {
		t.Fatal(err)
	}
	expected := SourceExpectation{RepositoryID: 123, CommitSHA: commit, SHA256: digest}
	packed := publicArchiveFixture(t, source, commit, files)
	c := GitHubClient{Client: &http.Client{Transport: githubTransport(func(r *http.Request) (*http.Response, error) {
		return &http.Response{StatusCode: 200, Body: io.NopCloser(bytes.NewReader(packed))}, nil
	})}}
	got, err := c.CapturePinned(context.Background(), source, expected, "")
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	archivePath := filepath.Join(dir, "source.tar.gz")
	if err := os.WriteFile(archivePath, packed, 0600); err != nil {
		t.Fatal(err)
	}
	input, _ := json.Marshal(map[string]any{"source": source, "expected": expected, "archive": archivePath})
	command := exec.Command("node", "-e", `const fs=require('node:fs');const sdk=require('../sdk/index.cjs');const input=JSON.parse(fs.readFileSync(0,'utf8'));sdk.capturePinnedSource(input.source,input.expected,'',{fetch:async()=>new Response(fs.readFileSync(input.archive))}).then(result=>process.stdout.write(JSON.stringify(result))).catch(error=>{console.error(error);process.exitCode=1;});`)
	command.Stdin = bytes.NewReader(input)
	output, err := command.CombinedOutput()
	if err != nil {
		t.Fatalf("Node parity: %v %s", err, output)
	}
	var other Snapshot
	if err := json.Unmarshal(output, &other); err != nil {
		t.Fatal(err)
	}
	a, _ := json.Marshal(got)
	b, _ := json.Marshal(other)
	if !bytes.Equal(a, b) {
		t.Fatalf("language snapshots differ: %s / %s", a, b)
	}
}
