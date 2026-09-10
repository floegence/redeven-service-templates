package template

import (
	"context"
	"crypto/sha1" // Git object addressing; SHA-256 protects the complete source directory.
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"path"
	"regexp"
	"slices"
	"strings"
	"time"
)

type GitSource struct {
	Repository string `json:"repository"`
	Ref        string `json:"ref,omitempty"`
	Path       string `json:"path,omitempty"`
}
type ResolvedSource struct {
	Repository   string `json:"repository"`
	RepositoryID int64  `json:"repository_id"`
	Ref          string `json:"ref"`
	Path         string `json:"path"`
	CommitSHA    string `json:"commit_sha"`
	TreeSHA      string `json:"tree_sha,omitempty"`
}
type Snapshot struct {
	Source ResolvedSource `json:"source"`
	Files  []File         `json:"files"`
	SHA256 string         `json:"sha256"`
}
type SourceEntry struct {
	Path       string `json:"path"`
	Entrypoint string `json:"entrypoint"`
}
type SourceCatalog struct {
	Source    ResolvedSource `json:"source"`
	Templates []SourceEntry  `json:"templates"`
}

var repositoryPattern = regexp.MustCompile(`^[a-zA-Z0-9_.-]+/[a-zA-Z0-9_.-]+$`)
var gitSHAPattern = regexp.MustCompile(`^[a-f0-9]{40}$`)

func (snapshot Snapshot) Validate() error {
	source := snapshot.Source
	if !repositoryPattern.MatchString(source.Repository) || source.RepositoryID < 1 || !gitSHAPattern.MatchString(source.CommitSHA) || !validRef(source.Ref) || (source.Path != "" && !validPath(source.Path)) {
		return fail("TEMPLATE_SOURCE_INVALID", "The template source identity is invalid.")
	}
	digest, err := Digest(snapshot.Files)
	if err != nil {
		return err
	}
	if digest != snapshot.SHA256 {
		return fail("TEMPLATE_SOURCE_DIGEST_MISMATCH", "The template source changed during transfer.")
	}
	return nil
}

func validRef(value string) bool {
	return value != "" && len(value) <= 256 && !strings.ContainsAny(value, "\x00\r\n\\?#") && !strings.Contains(value, "..")
}

// GitHubClient uses only the GitHub HTTPS API. Client may provide a host-owned
// transport; redirects always fail closed and never forward credentials.
type GitHubClient struct{ Client *http.Client }

func (c *GitHubClient) request(ctx context.Context, repository, endpoint, token string, target any) error {
	client := http.Client{Timeout: 45 * time.Second}
	if c != nil && c.Client != nil {
		client = *c.Client
		if client.Timeout == 0 {
			client.Timeout = 45 * time.Second
		}
	}
	client.CheckRedirect = func(*http.Request, []*http.Request) error {
		return fail("TEMPLATE_SOURCE_REDIRECT_REJECTED", "GitHub API redirects are not accepted.")
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, "https://api.github.com/repos/"+repository+endpoint, nil)
	if err != nil {
		return fail("TEMPLATE_SOURCE_INVALID", "The GitHub source request is invalid.")
	}
	request.Header.Set("Accept", "application/vnd.github+json")
	request.Header.Set("X-GitHub-Api-Version", "2022-11-28")
	request.Header.Set("User-Agent", "Redeven-Service-Templates")
	if token != "" {
		request.Header.Set("Authorization", "Bearer "+token)
	}
	response, err := client.Do(request)
	if err != nil {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		return fail("TEMPLATE_SOURCE_UNREACHABLE", "GitHub could not be reached from the selected download location.")
	}
	defer response.Body.Close()
	switch response.StatusCode {
	case 200:
	case 401:
		return fail("TEMPLATE_SOURCE_AUTH_REQUIRED", "GitHub rejected the temporary repository credential.")
	case 403, 429:
		if response.StatusCode == 429 || response.Header.Get("X-RateLimit-Remaining") == "0" || response.Header.Get("Retry-After") != "" {
			return fail("TEMPLATE_SOURCE_RATE_LIMITED", "GitHub has temporarily limited repository requests. Try again later.")
		}
		return fail("TEMPLATE_SOURCE_ACCESS_DENIED", "GitHub denied access. Verify the token's repository read permission.")
	case 404:
		return fail("TEMPLATE_SOURCE_NOT_FOUND", "The repository, ref, or template path was not found, or the credential cannot access it.")
	default:
		return fail("TEMPLATE_SOURCE_UNAVAILABLE", "GitHub could not return the template source.")
	}
	const limit = 6 * 1024 * 1024
	raw, err := io.ReadAll(io.LimitReader(response.Body, limit+1))
	if err != nil {
		return fail("TEMPLATE_SOURCE_UNREACHABLE", "The GitHub response was interrupted.")
	}
	if len(raw) > limit {
		return fail("TEMPLATE_SOURCE_LIMIT", "The GitHub response exceeds the template source limit.")
	}
	if err := json.Unmarshal(raw, target); err != nil {
		return fail("TEMPLATE_SOURCE_RESPONSE_INVALID", "GitHub returned an invalid source response.")
	}
	return nil
}

func parseGitSource(source GitSource) (GitSource, []string, error) {
	repository := strings.TrimSpace(source.Repository)
	tail := []string{}
	if strings.Contains(repository, "://") {
		u, err := url.Parse(repository)
		if err != nil || u.Scheme != "https" || u.Host != "github.com" || u.User != nil || u.RawQuery != "" || u.Fragment != "" {
			return source, nil, fail("TEMPLATE_SOURCE_INVALID", "Use a GitHub HTTPS repository, directory, or template file link.")
		}
		parts := strings.Split(strings.Trim(u.Path, "/"), "/")
		if len(parts) < 2 {
			return source, nil, fail("TEMPLATE_SOURCE_INVALID", "A GitHub owner and repository are required.")
		}
		repository = strings.Join(parts[:2], "/")
		if len(parts) > 2 {
			if len(parts) < 4 || (parts[2] != "tree" && parts[2] != "blob") {
				return source, nil, fail("TEMPLATE_SOURCE_INVALID", "Use a repository path instead of a Release or archive link.")
			}
			tail = parts[3:]
		}
	}
	repository = strings.TrimSuffix(repository, ".git")
	if !repositoryPattern.MatchString(repository) || strings.Contains(repository, "..") || len(repository) > 240 {
		return source, nil, fail("TEMPLATE_SOURCE_INVALID", "The GitHub repository name is invalid.")
	}
	source.Repository = repository
	source.Ref = strings.TrimSpace(source.Ref)
	source.Path = strings.Trim(strings.TrimSpace(source.Path), "/")
	if source.Path == Filename || source.Path == "template.json" {
		source.Path = ""
	} else if strings.HasSuffix(source.Path, "/"+Filename) || strings.HasSuffix(source.Path, "/template.json") {
		source.Path = path.Dir(source.Path)
	}
	if (source.Path != "" && !validPath(source.Path)) || (source.Ref != "" && !validRef(source.Ref)) || len(tail) > 32 {
		return source, nil, fail("TEMPLATE_SOURCE_INVALID", "The GitHub ref or template path is invalid.")
	}
	for _, part := range tail {
		if !validPath(part) {
			return source, nil, fail("TEMPLATE_SOURCE_INVALID", "The GitHub link path is invalid.")
		}
	}
	return source, tail, nil
}

func (c *GitHubClient) Resolve(ctx context.Context, input GitSource, token string) (ResolvedSource, error) {
	source, tail, err := parseGitSource(input)
	if err != nil {
		return ResolvedSource{}, err
	}
	var repo struct {
		ID            int64  `json:"id"`
		FullName      string `json:"full_name"`
		DefaultBranch string `json:"default_branch"`
	}
	if err := c.request(ctx, source.Repository, "", token, &repo); err != nil {
		return ResolvedSource{}, err
	}
	if repo.ID < 1 || !repositoryPattern.MatchString(repo.FullName) || !strings.EqualFold(repo.FullName, source.Repository) || !validRef(repo.DefaultBranch) {
		return ResolvedSource{}, fail("TEMPLATE_SOURCE_RESPONSE_INVALID", "GitHub returned an invalid repository identity.")
	}
	var commit struct {
		SHA    string `json:"sha"`
		Commit struct {
			Tree struct {
				SHA string `json:"sha"`
			} `json:"tree"`
		} `json:"commit"`
	}
	if len(tail) > 0 {
		if source.Ref != "" || source.Path != "" {
			return ResolvedSource{}, fail("TEMPLATE_SOURCE_INVALID", "Use the GitHub path link or separate ref and path fields, not both.")
		}
		found := false
		for cut := len(tail); cut > 0; cut-- {
			candidate := strings.Join(tail[:cut], "/")
			if !validRef(candidate) {
				continue
			}
			err := c.request(ctx, source.Repository, "/commits/"+url.PathEscape(candidate), token, &commit)
			if err != nil {
				if typed, ok := err.(*Error); ok && typed.Code == "TEMPLATE_SOURCE_NOT_FOUND" {
					continue
				}
				return ResolvedSource{}, err
			}
			source.Ref = candidate
			source.Path = strings.Join(tail[cut:], "/")
			found = true
			break
		}
		if !found {
			return ResolvedSource{}, fail("TEMPLATE_SOURCE_NOT_FOUND", "The GitHub link does not resolve to an accessible ref.")
		}
		if source.Path == Filename || source.Path == "template.json" {
			source.Path = ""
		} else if strings.HasSuffix(source.Path, "/"+Filename) || strings.HasSuffix(source.Path, "/template.json") {
			source.Path = path.Dir(source.Path)
		}
	} else {
		if source.Ref == "" {
			source.Ref = repo.DefaultBranch
		}
		if err := c.request(ctx, source.Repository, "/commits/"+url.PathEscape(source.Ref), token, &commit); err != nil {
			return ResolvedSource{}, err
		}
	}
	if !gitSHAPattern.MatchString(commit.SHA) || !gitSHAPattern.MatchString(commit.Commit.Tree.SHA) {
		return ResolvedSource{}, fail("TEMPLATE_SOURCE_RESPONSE_INVALID", "GitHub returned an invalid commit identity.")
	}
	return ResolvedSource{Repository: repo.FullName, RepositoryID: repo.ID, Ref: source.Ref, Path: source.Path, CommitSHA: commit.SHA, TreeSHA: commit.Commit.Tree.SHA}, nil
}

type gitTreeEntry struct {
	Path string `json:"path"`
	Mode string `json:"mode"`
	Type string `json:"type"`
	SHA  string `json:"sha"`
	Size int    `json:"size"`
}

func (c *GitHubClient) tree(ctx context.Context, source ResolvedSource, sha, token string) ([]gitTreeEntry, error) {
	if !gitSHAPattern.MatchString(sha) {
		return nil, fail("TEMPLATE_SOURCE_RESPONSE_INVALID", "GitHub returned an invalid directory identity.")
	}
	var tree struct {
		SHA       string         `json:"sha"`
		Tree      []gitTreeEntry `json:"tree"`
		Truncated bool           `json:"truncated"`
	}
	if err := c.request(ctx, source.Repository, "/git/trees/"+sha, token, &tree); err != nil {
		return nil, err
	}
	if tree.Truncated || len(tree.Tree) > MaxFiles {
		return nil, fail("TEMPLATE_SOURCE_LIMIT", "The repository directory listing is incomplete or exceeds the template limit.")
	}
	if tree.SHA != sha {
		return nil, fail("TEMPLATE_SOURCE_RESPONSE_INVALID", "GitHub returned a different directory identity.")
	}
	return tree.Tree, nil
}

func (c *GitHubClient) directory(ctx context.Context, source ResolvedSource, token string) ([]gitTreeEntry, error) {
	entries, err := c.tree(ctx, source, source.TreeSHA, token)
	if err != nil {
		return nil, err
	}
	if source.Path == "" {
		return entries, nil
	}
	for _, part := range strings.Split(source.Path, "/") {
		found := false
		for _, entry := range entries {
			if entry.Path == part && entry.Type == "tree" && gitSHAPattern.MatchString(entry.SHA) {
				entries, err = c.tree(ctx, source, entry.SHA, token)
				if err != nil {
					return nil, err
				}
				found = true
				break
			}
		}
		if !found {
			return nil, fail("TEMPLATE_SOURCE_NOT_FOUND", "The selected template directory does not exist in this commit.")
		}
	}
	return entries, nil
}

func (c *GitHubClient) Discover(ctx context.Context, input GitSource, token string) (SourceCatalog, error) {
	source, err := c.Resolve(ctx, input, token)
	if err != nil {
		return SourceCatalog{}, err
	}
	entries, err := c.directory(ctx, source, token)
	if err != nil {
		return SourceCatalog{}, err
	}
	result := SourceCatalog{Source: source, Templates: []SourceEntry{}}
	entrypoint := func(items []gitTreeEntry) string {
		for _, name := range []string{Filename, "template.json"} {
			for _, item := range items {
				if item.Path == name && item.Type == "blob" {
					return name
				}
			}
		}
		return ""
	}
	if name := entrypoint(entries); name != "" {
		result.Templates = append(result.Templates, SourceEntry{Path: source.Path, Entrypoint: name})
		return result, nil
	}
	for _, entry := range entries {
		if entry.Path != "templates" || entry.Type != "tree" {
			continue
		}
		children, err := c.tree(ctx, source, entry.SHA, token)
		if err != nil {
			return SourceCatalog{}, err
		}
		if len(children) > 100 {
			return SourceCatalog{}, fail("TEMPLATE_SOURCE_LIMIT", "Choose an explicit template path in repositories with more than 100 template directories.")
		}
		for _, child := range children {
			if child.Type != "tree" || !validPath(child.Path) {
				continue
			}
			items, err := c.tree(ctx, source, child.SHA, token)
			if err != nil {
				return SourceCatalog{}, err
			}
			if name := entrypoint(items); name != "" {
				result.Templates = append(result.Templates, SourceEntry{Path: path.Join(source.Path, "templates", child.Path), Entrypoint: name})
			}
		}
	}
	return result, nil
}

func (c *GitHubClient) Capture(ctx context.Context, input GitSource, token string) (Snapshot, error) {
	source, err := c.Resolve(ctx, input, token)
	if err != nil {
		return Snapshot{}, err
	}
	return c.CaptureResolved(ctx, source, token)
}
func (c *GitHubClient) CaptureResolved(ctx context.Context, source ResolvedSource, token string) (Snapshot, error) {
	if !repositoryPattern.MatchString(source.Repository) || source.RepositoryID < 1 || !validRef(source.Ref) || !gitSHAPattern.MatchString(source.TreeSHA) || !gitSHAPattern.MatchString(source.CommitSHA) || (source.Path != "" && !validPath(source.Path)) {
		return Snapshot{}, fail("TEMPLATE_SOURCE_INVALID", "The resolved template source is invalid.")
	}
	entries, err := c.directory(ctx, source, token)
	if err != nil {
		return Snapshot{}, err
	}
	files := []File{}
	size := 0
	var walk func([]gitTreeEntry, string, int) error
	walk = func(entries []gitTreeEntry, prefix string, depth int) error {
		if depth > 32 {
			return fail("TEMPLATE_SOURCE_LIMIT", "The template directory nesting exceeds its limit.")
		}
		for _, entry := range entries {
			name := path.Join(prefix, entry.Path)
			if !validPath(entry.Path) || !validPath(name) || !gitSHAPattern.MatchString(entry.SHA) {
				return fail("TEMPLATE_SOURCE_PATH_INVALID", "GitHub returned an invalid source path or object identity.")
			}
			if entry.Type == "tree" && entry.Mode == "040000" {
				children, err := c.tree(ctx, source, entry.SHA, token)
				if err != nil {
					return err
				}
				if err := walk(children, name, depth+1); err != nil {
					return err
				}
				continue
			}
			if entry.Type != "blob" || (entry.Mode != "100644" && entry.Mode != "100755") {
				return fail("TEMPLATE_SOURCE_PATH_INVALID", "Template sources cannot contain symlinks or submodules.")
			}
			size += entry.Size
			if entry.Size < 0 || entry.Size > MaxFileBytes || size > MaxDirectoryBytes || len(files) >= MaxFiles {
				return fail("TEMPLATE_SOURCE_LIMIT", "The template directory exceeds its file or byte limit.")
			}
			var blob struct {
				SHA      string `json:"sha"`
				Size     int    `json:"size"`
				Encoding string `json:"encoding"`
				Content  string `json:"content"`
			}
			if err := c.request(ctx, source.Repository, "/git/blobs/"+entry.SHA, token, &blob); err != nil {
				return err
			}
			data, err := base64.StdEncoding.DecodeString(blob.Content)
			if err != nil || blob.Encoding != "base64" || blob.SHA != entry.SHA || blob.Size != entry.Size || len(data) != entry.Size {
				return fail("TEMPLATE_SOURCE_RESPONSE_INVALID", "GitHub returned inconsistent file bytes.")
			}
			hash := sha1.New()
			fmt.Fprintf(hash, "blob %d\x00", len(data))
			hash.Write(data)
			if fmt.Sprintf("%x", hash.Sum(nil)) != entry.SHA {
				return fail("TEMPLATE_SOURCE_DIGEST_MISMATCH", "A GitHub blob did not match its declared identity.")
			}
			files = append(files, File{Path: name, Mode: entry.Mode, Content: data})
		}
		return nil
	}
	if err := walk(entries, "", 0); err != nil {
		return Snapshot{}, err
	}
	slices.SortFunc(files, func(a, b File) int { return strings.Compare(a.Path, b.Path) })
	digest, err := Digest(files)
	if err != nil {
		return Snapshot{}, err
	}
	snapshot := Snapshot{Source: source, Files: files, SHA256: digest}
	return snapshot, snapshot.Validate()
}
