package releasewatcher

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"maps"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"regexp"
	"strconv"
	"strings"

	"github.com/floegence/redeven-service-templates/internal/cataloggen"
)

type validation func(context.Context, string) error

// Apply validates the complete candidate in a temporary tree before changing
// any checkout bytes. Network, generator and test failures leave it untouched.
func Apply(ctx context.Context, root string, plan ReleasePlan) (bool, error) {
	return apply(ctx, root, plan, validateCandidate)
}

func apply(ctx context.Context, root string, plan ReleasePlan, validate validation) (bool, error) {
	config, err := LoadConfig(root)
	if err != nil {
		return false, err
	}
	if len(plan.Releases) != len(config.Sources) {
		return false, errors.New("release plan source count mismatch")
	}
	originals := map[string][]byte{}
	updates := map[string][]byte{}
	for i, release := range plan.Releases {
		source := config.Sources[i]
		if !reflect.DeepEqual(source, release.Source) || !validVersion(release.Version) {
			return false, errors.New("release plan source mismatch")
		}
		relative := "templates/" + source.TemplateID + "/redeven-service-template.json"
		data, err := os.ReadFile(filepath.Join(root, relative))
		if err != nil {
			return false, err
		}
		var current cataloggen.TemplateDefinition
		if err := decodeJSON(data, &current); err != nil {
			return false, err
		}
		if current.TemplateID != source.TemplateID || current.Revision < 1 || current.ReleaseDiscovery == nil || current.ReleaseDiscovery.Source != source.Kind {
			return false, errors.New("template source or identity mismatch")
		}
		if !validVersion(current.RecommendedVersion) || compareVersions(release.Version, current.RecommendedVersion) < 0 {
			return false, errors.New("upstream recommendation regressed")
		}
		fields := map[string]any{}
		switch source.Kind {
		case "npm":
			var spec struct {
				Host struct {
					NPM struct {
						PackageName string `json:"package_name"`
						RegistryURL string `json:"registry_url"`
						Version     string `json:"version"`
					} `json:"npm"`
				} `json:"host"`
			}
			if json.Unmarshal(current.Spec, &spec) != nil || current.Deployment != "host" || spec.Host.NPM.PackageName != source.PackageName || trimRegistry(spec.Host.NPM.RegistryURL) != trimRegistry(source.RegistryURL) || !validNPMIntegrity(release.Integrity) {
				return false, errors.New("npm template coordinates or integrity mismatch")
			}
			if spec.Host.NPM.Version != release.Version {
				fields["spec.host.npm.version"] = release.Version
			}
		case "oci":
			if current.Deployment != "container" || len(release.Artifacts) != len(source.Platforms) {
				return false, errors.New("OCI artifact count mismatch")
			}
			for _, platform := range source.Platforms {
				artifact := release.Artifacts[platform]
				prefix := source.image() + ":" + release.Version + "@"
				if !strings.HasPrefix(artifact, prefix) || !digestRE.MatchString(strings.TrimPrefix(artifact, prefix)) {
					return false, errors.New("OCI artifact identity mismatch")
				}
			}
			if !maps.Equal(current.PlatformArtifacts, release.Artifacts) {
				if current.RecommendedVersion == release.Version {
					return false, errors.New("upstream changed an immutable tag digest")
				}
				fields["platform_artifacts"] = release.Artifacts
			}
		}
		if current.RecommendedVersion != release.Version {
			fields["recommended_version"] = release.Version
		}
		if len(fields) == 0 {
			continue
		}
		if current.Revision == int(^uint(0)>>1) {
			return false, errors.New("template revision overflow")
		}
		fields["revision"] = current.Revision + 1
		updated := data
		for field, value := range fields {
			encoded, err := json.Marshal(value)
			if err != nil {
				return false, err
			}
			// Preserve all unrelated source formatting and key order.
			updated, err = patchJSON(updated, strings.Split(field, "."), encoded)
			if err != nil {
				return false, err
			}
		}
		originals[relative], updates[relative] = data, updated
	}
	if len(updates) == 0 {
		return false, nil
	}
	for _, relative := range []string{"dist/catalog.bundle.json", "dist/catalog.bundle.manifest.json", "dist/catalog.bundle.sha256"} {
		originals[relative], err = os.ReadFile(filepath.Join(root, relative))
		if err != nil {
			return false, err
		}
	}
	version, constants, err := catalogConstants(root)
	if err != nil {
		return false, err
	}
	parts := strings.Split(strings.TrimPrefix(version, "v"), ".")
	patch, err := strconv.ParseUint(parts[2], 10, 63)
	if err != nil {
		return false, err
	}
	next := fmt.Sprintf("v%s.%s.%d", parts[0], parts[1], patch+1)
	for relative, data := range constants {
		originals[relative] = data
		updates[relative] = bytes.Replace(data, []byte(`"`+version+`"`), []byte(`"`+next+`"`), 1)
	}
	staged, err := os.MkdirTemp("", "service-template-candidate-")
	if err != nil {
		return false, err
	}
	defer os.RemoveAll(staged)
	if err := copyTree(root, staged); err != nil {
		return false, err
	}
	for relative, data := range updates {
		if err := os.WriteFile(filepath.Join(staged, relative), data, 0644); err != nil {
			return false, err
		}
	}
	if err := cataloggen.HasForbiddenCompatibilityCode(staged); err != nil {
		return false, err
	}
	output, err := cataloggen.GenerateWithVersion(staged, next)
	if err != nil {
		return false, err
	}
	if err := cataloggen.Write(staged, output); err != nil {
		return false, err
	}
	if err := cataloggen.Verify(staged, output); err != nil {
		return false, err
	}
	if err := validate(ctx, staged); err != nil {
		return false, fmt.Errorf("candidate validation: %w", err)
	}
	for _, relative := range []string{"dist/catalog.bundle.json", "dist/catalog.bundle.manifest.json", "dist/catalog.bundle.sha256"} {
		updates[relative], err = os.ReadFile(filepath.Join(staged, relative))
		if err != nil {
			return false, err
		}
	}
	if err := ctx.Err(); err != nil {
		return false, err
	}
	if err := replaceFiles(root, originals, updates, os.Rename); err != nil {
		return false, err
	}
	return true, nil
}

func validateCandidate(ctx context.Context, root string) error {
	for _, args := range [][]string{{"run", "./cmd/build-catalog", "--check-registry", "--verify"}, {"test", "./..."}} {
		command := exec.CommandContext(ctx, "go", args...)
		command.Dir = root
		command.Env = append(os.Environ(), "GOWORK=off")
		command.Stdout, command.Stderr = os.Stdout, os.Stderr
		if err := command.Run(); err != nil {
			return err
		}
	}
	return nil
}

func catalogConstants(root string) (string, map[string][]byte, error) {
	result := map[string][]byte{}
	version := ""
	for _, item := range []struct{ path, name string }{{"catalog.go", "Version"}, {"internal/cataloggen/generate.go", "CatalogVersion"}} {
		data, err := os.ReadFile(filepath.Join(root, item.path))
		if err != nil {
			return "", nil, err
		}
		matches := regexp.MustCompile("(?m)^const "+item.name+` = "(v[0-9]+\.[0-9]+\.[0-9]+)"$`).FindAllSubmatch(data, -1)
		if len(matches) != 1 || !validVersion(strings.TrimPrefix(string(matches[0][1]), "v")) {
			return "", nil, errors.New("invalid catalog version constant")
		}
		value := string(matches[0][1])
		if version != "" && version != value {
			return "", nil, errors.New("catalog version constants do not match")
		}
		version = value
		result[item.path] = data
	}
	return version, result, nil
}

func patchJSON(data []byte, keys []string, replacement []byte) ([]byte, error) {
	decoder := json.NewDecoder(bytes.NewReader(data))
	token, err := decoder.Token()
	if err != nil || token != json.Delim('{') {
		return nil, errors.New("JSON patch requires an object")
	}
	start, end, count := 0, 0, 0
	for decoder.More() {
		key, err := decoder.Token()
		if err != nil {
			return nil, err
		}
		var value json.RawMessage
		if err := decoder.Decode(&value); err != nil {
			return nil, err
		}
		if key == keys[0] {
			count++
			end = int(decoder.InputOffset())
			start = end - len(value)
		}
	}
	if count != 1 {
		return nil, fmt.Errorf("JSON field %s is missing or duplicated", keys[0])
	}
	if len(keys) > 1 {
		replacement, err = patchJSON(data[start:end], keys[1:], replacement)
		if err != nil {
			return nil, err
		}
	}
	result := append([]byte{}, data[:start]...)
	result = append(result, replacement...)
	return append(result, data[end:]...), nil
}

func copyTree(source, destination string) error {
	return filepath.WalkDir(source, func(p string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		relative, err := filepath.Rel(source, p)
		if err != nil {
			return err
		}
		if relative == ".git" {
			if entry.IsDir() {
				return filepath.SkipDir
			}
			return nil
		}
		target := filepath.Join(destination, relative)
		if entry.IsDir() {
			return os.MkdirAll(target, 0755)
		}
		info, err := entry.Info()
		if err != nil {
			return err
		}
		if !info.Mode().IsRegular() {
			return fmt.Errorf("non-regular staging input: %s", relative)
		}
		data, err := os.ReadFile(p)
		if err != nil {
			return err
		}
		return os.WriteFile(target, data, info.Mode().Perm())
	})
}

// Prepare every rename before mutating destinations, then restore originals
// on a rename failure. The workflow's separate worktree is the publication boundary.
func replaceFiles(root string, originals, updates map[string][]byte, rename func(string, string) error) error {
	temporary := map[string]string{}
	defer func() {
		for _, p := range temporary {
			_ = os.Remove(p)
		}
	}()
	for relative, data := range updates {
		p := filepath.Join(root, relative)
		current, err := os.ReadFile(p)
		if err != nil {
			return err
		}
		if !bytes.Equal(current, originals[relative]) {
			return fmt.Errorf("checkout changed during validation: %s", relative)
		}
		file, err := os.CreateTemp(filepath.Dir(p), ".release-watcher-")
		if err != nil {
			return err
		}
		temporary[relative] = file.Name()
		_, writeErr := file.Write(data)
		chmodErr := file.Chmod(0644)
		closeErr := file.Close()
		if err := errors.Join(writeErr, chmodErr, closeErr); err != nil {
			return err
		}
	}
	committed := []string{}
	for relative, temp := range temporary {
		if err := rename(temp, filepath.Join(root, relative)); err != nil {
			failures := []error{err}
			for _, prior := range committed {
				failures = append(failures, os.WriteFile(filepath.Join(root, prior), originals[prior], 0644))
			}
			return errors.Join(failures...)
		}
		committed = append(committed, relative)
	}
	return nil
}
