package template

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"slices"
	"strings"
	"unicode"
	"unicode/utf8"
)

const (
	MaxFileBytes      = 2 * 1024 * 1024
	MaxDirectoryBytes = 16 * 1024 * 1024
	MaxFiles          = 1024
)

// File carries exact Git blob bytes and its regular-file executable mode.
// Content is base64 in the JSON transfer representation, never rewritten text.
type File struct {
	Path    string `json:"path"`
	Mode    string `json:"mode"`
	Content []byte `json:"content"`
}

func validPath(value string) bool {
	if value == "" || !utf8.ValidString(value) || !fs.ValidPath(value) || value == "." || strings.Contains(value, "\\") || strings.Contains(value, ":") || len(value) > 1024 {
		return false
	}
	for _, c := range value {
		if unicode.IsControl(c) {
			return false
		}
	}
	for _, part := range strings.Split(value, "/") {
		if strings.HasSuffix(part, ".") || strings.HasSuffix(part, " ") || strings.EqualFold(part, ".git") {
			return false
		}
	}
	return true
}

func Digest(files []File) (string, error) {
	if len(files) == 0 || len(files) > MaxFiles {
		return "", fail("TEMPLATE_SOURCE_LIMIT", "Template file count exceeds the supported limit.")
	}
	ordered := append([]File(nil), files...)
	slices.SortFunc(ordered, func(a, b File) int { return strings.Compare(a.Path, b.Path) })
	seen := map[string]bool{}
	total := 0
	hash := sha256.New()
	for _, file := range ordered {
		if !validPath(file.Path) || (file.Mode != "100644" && file.Mode != "100755") {
			return "", fail("TEMPLATE_SOURCE_PATH_INVALID", "Template sources must contain portable relative paths and regular files.")
		}
		key := strings.ToLower(file.Path)
		if seen[key] {
			return "", fail("TEMPLATE_SOURCE_PATH_CONFLICT", "Template source paths conflict.")
		}
		seen[key] = true
		total += len(file.Content)
		if len(file.Content) > MaxFileBytes || total > MaxDirectoryBytes {
			return "", fail("TEMPLATE_SOURCE_LIMIT", "Template source size exceeds the supported limit.")
		}
		if bytes.HasPrefix(file.Content, []byte("version https://git-lfs.github.com/spec/v1")) {
			return "", fail("TEMPLATE_SOURCE_LFS_UNSUPPORTED", "Template sources must contain file bytes instead of Git LFS pointers.")
		}
		sum := sha256.Sum256(file.Content)
		fmt.Fprintf(hash, "%s\x00%s\x00%x\n", file.Path, file.Mode, sum)
	}
	for key := range seen {
		for parent := path.Dir(key); parent != "."; parent = path.Dir(parent) {
			if seen[parent] {
				return "", fail("TEMPLATE_SOURCE_PATH_CONFLICT", "A template file is also used as a directory.")
			}
		}
	}
	return hex.EncodeToString(hash.Sum(nil)), nil
}

func ReadDirectory(root string) ([]File, error) {
	info, err := os.Lstat(root)
	if err != nil {
		return nil, err
	}
	if !info.IsDir() {
		return nil, fail("TEMPLATE_SOURCE_PATH_INVALID", "Template root must be a directory.")
	}
	files := []File{}
	total := int64(0)
	err = filepath.WalkDir(root, func(filename string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if entry.IsDir() {
			return nil
		}
		info, err := entry.Info()
		if err != nil {
			return err
		}
		if !info.Mode().IsRegular() {
			return fail("TEMPLATE_SOURCE_PATH_INVALID", "Template sources must contain regular files.")
		}
		total += info.Size()
		if info.Size() > MaxFileBytes || total > MaxDirectoryBytes || len(files) >= MaxFiles {
			return fail("TEMPLATE_SOURCE_LIMIT", "Template source exceeds its published limits.")
		}
		rel, err := filepath.Rel(root, filename)
		if err != nil {
			return err
		}
		in, err := os.Open(filename)
		if err != nil {
			return err
		}
		data, readErr := io.ReadAll(io.LimitReader(in, MaxFileBytes+1))
		in.Close()
		if readErr != nil {
			return readErr
		}
		mode := "100644"
		if info.Mode()&0111 != 0 {
			mode = "100755"
		}
		files = append(files, File{Path: filepath.ToSlash(rel), Mode: mode, Content: data})
		return nil
	})
	if err != nil {
		return nil, err
	}
	if _, err = Digest(files); err != nil {
		return nil, err
	}
	return files, nil
}

// WriteDirectory creates a new, private source directory. It never overwrites
// an existing source; hosts select a completely verified directory atomically.
func WriteDirectory(root string, files []File) error {
	if _, err := Digest(files); err != nil {
		return err
	}
	if err := os.Mkdir(root, 0700); err != nil {
		return err
	}
	directories := map[string]bool{root: true, filepath.Dir(root): true}
	for _, file := range files {
		filename := filepath.Join(root, filepath.FromSlash(file.Path))
		if err := os.MkdirAll(filepath.Dir(filename), 0700); err != nil {
			return err
		}
		for dir := filepath.Dir(filename); dir != root; dir = filepath.Dir(dir) {
			directories[dir] = true
		}
		mode := fs.FileMode(0600)
		if file.Mode == "100755" {
			mode = 0700
		}
		out, err := os.OpenFile(filename, os.O_CREATE|os.O_EXCL|os.O_WRONLY, mode)
		if err != nil {
			return err
		}
		_, writeErr := out.Write(file.Content)
		syncErr := out.Sync()
		closeErr := out.Close()
		if writeErr != nil {
			return writeErr
		}
		if syncErr != nil {
			return syncErr
		}
		if closeErr != nil {
			return closeErr
		}
	}
	ordered := []string{}
	for dir := range directories {
		ordered = append(ordered, dir)
	}
	slices.SortFunc(ordered, func(a, b string) int { return len(b) - len(a) })
	for _, dir := range ordered {
		in, err := os.Open(dir)
		if err != nil {
			return err
		}
		syncErr := in.Sync()
		closeErr := in.Close()
		if syncErr != nil {
			return syncErr
		}
		if closeErr != nil {
			return closeErr
		}
	}
	return nil
}
