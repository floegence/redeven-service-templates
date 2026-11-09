package template

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"errors"
	"io"
	"net/http"
	"slices"
	"strings"
	"time"
)

// A public pinned archive is selected by an existing expected identity. The
// directory digest, not a new API lookup, authenticates its complete bytes.
// No archive entry is extracted to disk and no credential reaches codeload.
func (c *GitHubClient) capturePublicArchive(ctx context.Context, source GitSource, expected SourceExpectation) (Snapshot, error) {
	ctx, cancel := context.WithTimeout(ctx, 45*time.Second)
	defer cancel()
	if err := ctx.Err(); err != nil {
		return Snapshot{}, err
	}
	invalid := func() error {
		return fail("TEMPLATE_SOURCE_RESPONSE_INVALID", "GitHub returned an invalid source archive.")
	}
	limit := func() error {
		return fail("TEMPLATE_SOURCE_LIMIT", "The source archive exceeds its bounded download limit.")
	}
	client := http.Client{Timeout: 45 * time.Second}
	if c != nil && c.Client != nil {
		client = *c.Client
	}
	client.CheckRedirect = func(*http.Request, []*http.Request) error {
		return errors.New("source archive redirects are not accepted")
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, "https://codeload.github.com/"+source.Repository+"/tar.gz/"+expected.CommitSHA, nil)
	if err != nil {
		return Snapshot{}, invalid()
	}
	request.Header.Set("User-Agent", "Redeven-Service-Templates")
	response, err := client.Do(request)
	if err != nil {
		if ctx.Err() != nil {
			return Snapshot{}, ctx.Err()
		}
		return Snapshot{}, fail("TEMPLATE_SOURCE_UNREACHABLE", "GitHub could not be reached from the selected download location.")
	}
	defer response.Body.Close()
	switch response.StatusCode {
	case 200:
	case 404:
		return Snapshot{}, fail("TEMPLATE_SOURCE_NOT_FOUND", "The public commit archive was not found. Private repositories require a temporary token.")
	case 429:
		return Snapshot{}, fail("TEMPLATE_SOURCE_RATE_LIMITED", "GitHub has temporarily limited repository requests. Try again later.")
	default:
		return Snapshot{}, fail("TEMPLATE_SOURCE_UNAVAILABLE", "GitHub could not return the public source archive.")
	}
	if response.ContentLength > 64<<20 {
		return Snapshot{}, limit()
	}
	packed, err := io.ReadAll(io.LimitReader(response.Body, (64<<20)+1))
	if err != nil {
		if ctx.Err() != nil {
			return Snapshot{}, ctx.Err()
		}
		return Snapshot{}, fail("TEMPLATE_SOURCE_UNREACHABLE", "The source archive download was interrupted.")
	}
	if len(packed) > 64<<20 {
		return Snapshot{}, limit()
	}
	gz, err := gzip.NewReader(bytes.NewReader(packed))
	if err != nil {
		return Snapshot{}, invalid()
	}
	defer gz.Close()
	raw, err := io.ReadAll(io.LimitReader(gz, (128<<20)+1))
	if err != nil {
		return Snapshot{}, invalid()
	}
	if len(raw) > 128<<20 {
		return Snapshot{}, limit()
	}
	if len(raw)%512 != 0 {
		return Snapshot{}, invalid()
	}
	rawReader := bytes.NewReader(raw)
	reader := tar.NewReader(rawReader)
	root := strings.Split(source.Repository, "/")[1] + "-" + expected.CommitSHA
	prefix := root + "/"
	if source.Path != "" {
		prefix += source.Path + "/"
	}
	files := []File{}
	seen := map[string]bool{}
	total := int64(0)
	count := 0
	commit := ""
	for {
		if err := ctx.Err(); err != nil {
			return Snapshot{}, err
		}
		before := rawReader.Len()
		header, err := reader.Next()
		if errors.Is(err, io.EOF) {
			if before-rawReader.Len() < 1024 || !allZero(raw[len(raw)-rawReader.Len():]) {
				return Snapshot{}, invalid()
			}
			break
		}
		if err != nil {
			return Snapshot{}, invalid()
		}
		count++
		if count > 100000 {
			return Snapshot{}, limit()
		}
		if header.Format != tar.FormatUSTAR && header.Format != tar.FormatPAX {
			return Snapshot{}, invalid()
		}
		for key := range header.PAXRecords {
			if key != "comment" && key != "path" && key != "mtime" && key != "atime" && key != "ctime" {
				return Snapshot{}, invalid()
			}
		}
		if header.Typeflag != tar.TypeXGlobalHeader && header.PAXRecords["comment"] != "" {
			return Snapshot{}, invalid()
		}
		if header.Typeflag == tar.TypeXGlobalHeader {
			if count != 1 || commit != "" || len(files) > 0 {
				return Snapshot{}, invalid()
			}
			commit = header.PAXRecords["comment"]
			if header.PAXRecords["path"] != "" {
				return Snapshot{}, invalid()
			}
			continue
		}
		if commit != expected.CommitSHA {
			return Snapshot{}, fail("TEMPLATE_SOURCE_EXPECTATION_MISMATCH", "The archive commit differs from the selected identity.")
		}
		name := strings.TrimSuffix(header.Name, "/")
		if !validPath(name) || (name != root && !strings.HasPrefix(name, root+"/")) {
			return Snapshot{}, fail("TEMPLATE_SOURCE_PATH_INVALID", "The source archive contains an invalid path.")
		}
		if name == root || !strings.HasPrefix(header.Name, prefix) {
			continue
		}
		relative := strings.TrimPrefix(name, prefix)
		if relative == "" {
			continue
		}
		if header.Typeflag == tar.TypeDir {
			continue
		}
		if header.Typeflag != tar.TypeReg || !validPath(relative) || len(strings.Split(relative, "/")) > 33 {
			return Snapshot{}, fail("TEMPLATE_SOURCE_PATH_INVALID", "Template sources must contain portable relative paths and regular files.")
		}
		key := strings.ToLower(relative)
		if seen[key] {
			return Snapshot{}, fail("TEMPLATE_SOURCE_PATH_CONFLICT", "Template source paths conflict.")
		}
		seen[key] = true
		total += header.Size
		if header.Size < 0 || header.Size > 2<<20 || total > 16<<20 || len(files) >= 1024 {
			return Snapshot{}, limit()
		}
		data, err := io.ReadAll(reader)
		if err != nil || int64(len(data)) != header.Size {
			return Snapshot{}, invalid()
		}
		mode := "100644"
		if header.Mode&0111 != 0 {
			mode = "100755"
		}
		files = append(files, File{Path: relative, Mode: mode, Content: data})
	}
	slices.SortFunc(files, func(a, b File) int { return strings.Compare(a.Path, b.Path) })
	digest, err := Digest(files)
	if err != nil {
		return Snapshot{}, err
	}
	snapshot := Snapshot{Source: ResolvedSource{Repository: source.Repository, RepositoryID: expected.RepositoryID, Ref: source.Ref, Path: source.Path, CommitSHA: expected.CommitSHA}, Files: files, SHA256: digest}
	if err := expected.Verify(snapshot); err != nil {
		return Snapshot{}, err
	}
	return snapshot, nil
}

func allZero(data []byte) bool {
	for _, b := range data {
		if b != 0 {
			return false
		}
	}
	return true
}
