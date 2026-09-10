package template

import (
	"fmt"
	"gopkg.in/yaml.v3"
	"net/url"
	"path/filepath"
	"regexp"
	"strings"
)

var (
	templateNamePattern             = regexp.MustCompile(`^[^\x00-\x1f\x7f]{1,80}$`)
	templateParameterPattern        = regexp.MustCompile(`^[A-Z][A-Z0-9_]{0,63}$`)
	managedWorkspaceIdentityPattern = regexp.MustCompile(`^[a-zA-Z0-9][a-zA-Z0-9_.-]{0,127}$`)
	composeServicePattern           = regexp.MustCompile(`^[a-zA-Z0-9][a-zA-Z0-9_.-]{0,62}$`)
	composeVolumePattern            = regexp.MustCompile(`^[a-zA-Z0-9][a-zA-Z0-9_.-]{0,127}$`)
	npmPackagePattern               = regexp.MustCompile(`^(?:@[a-z0-9][a-z0-9._~-]*/)?[a-z0-9][a-z0-9._~-]*$`)
	npmExecutablePattern            = regexp.MustCompile(`^[a-zA-Z0-9][a-zA-Z0-9_.-]{0,127}$`)
	exactSemverPattern              = regexp.MustCompile(`^(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)(?:-[-0-9A-Za-z.]+)?(?:\+[-0-9A-Za-z.]+)?$`)
)

func ValidateSpec(spec Spec) error {
	if spec.SchemaVersion != SpecVersion {
		return serviceError("TEMPLATE_SCHEMA_UNSUPPORTED", "The template schema version is not supported.", 400, false, nil)
	}
	if spec.Endpoint.Scheme != "http" && spec.Endpoint.Scheme != "https" {
		return serviceError("TEMPLATE_ENDPOINT_INVALID", "Template endpoint scheme must be http or https.", 400, false, nil)
	}
	for _, value := range []string{spec.Endpoint.Path, spec.Endpoint.HealthPath} {
		if value != "" && (!strings.HasPrefix(value, "/") || strings.ContainsAny(value, "\x00\r\n")) {
			return serviceError("TEMPLATE_ENDPOINT_INVALID", "Template endpoint paths must begin with /.", 400, false, nil)
		}
	}
	if spec.Endpoint.StartupTimeout < 0 || spec.Endpoint.StartupTimeout > 600 {
		return serviceError("TEMPLATE_ENDPOINT_INVALID", "Template startup timeout must be between 0 and 600 seconds.", 400, false, nil)
	}
	if len(spec.Parameters) > 64 {
		return serviceError("TEMPLATE_PARAMETERS_INVALID", "A template can define at most 64 parameters.", 400, false, nil)
	}
	seen := map[string]struct{}{}
	for _, parameter := range spec.Parameters {
		if !templateParameterPattern.MatchString(parameter.Name) || len(parameter.Label) > 80 || len(parameter.Description) > 300 {
			return serviceError("TEMPLATE_PARAMETERS_INVALID", "A template parameter is invalid.", 400, false, nil)
		}
		if _, ok := seen[parameter.Name]; ok {
			return serviceError("TEMPLATE_PARAMETERS_INVALID", "Template parameter names must be unique.", 400, false, nil)
		}
		seen[parameter.Name] = struct{}{}
		if parameter.Type != "text" && parameter.Type != "number" && parameter.Type != "boolean" && parameter.Type != "secret" && parameter.Type != "path" {
			return serviceError("TEMPLATE_PARAMETERS_INVALID", "Template parameter type is invalid.", 400, false, nil)
		}
		if parameter.Type == "secret" && parameter.Default != "" {
			return serviceError("TEMPLATE_SECRET_DEFAULT_FORBIDDEN", "Secret template parameters cannot have default values.", 400, false, nil)
		}
	}
	switch spec.Kind {
	case DeploymentHost:
		if spec.Host == nil || strings.TrimSpace(spec.Host.StartScript) == "" {
			return serviceError("TEMPLATE_HOST_INVALID", "Host templates require a foreground start script.", 400, false, nil)
		}
		for _, script := range []string{spec.Host.InstallScript, spec.Host.StartScript, spec.Host.StopScript, spec.Host.UninstallScript, spec.Host.AfterStartScript, spec.Host.OpenScript} {
			if len(script) > 128*1024 || strings.ContainsRune(script, '\x00') {
				return serviceError("TEMPLATE_HOST_INVALID", "A host lifecycle script is too large or invalid.", 400, false, nil)
			}
		}
		if len(spec.Host.Environment) > 256 {
			return serviceError("TEMPLATE_HOST_INVALID", "A host template can define at most 256 environment variables.", 400, false, nil)
		}
		for key, value := range spec.Host.Environment {
			if !templateParameterPattern.MatchString(key) || strings.HasPrefix(key, "REDEVEN_") || strings.ContainsAny(value, "\x00\r\n") {
				return serviceError("TEMPLATE_HOST_INVALID", "A host environment variable is invalid or reserved.", 400, false, nil)
			}
		}
		managedInstallers := 0
		if spec.Host.Artifact != nil {
			managedInstallers++
		}
		if spec.Host.NPM != nil {
			managedInstallers++
		}
		if managedInstallers > 1 {
			return serviceError("TEMPLATE_HOST_PACKAGE_INVALID", "Host templates may declare one artifact or npm package.", 400, false, nil)
		}
		if spec.Host.NPM != nil {
			if err := ValidateNPMHostPackage(*spec.Host.NPM, spec.Parameters); err != nil {
				return err
			}
		}
		if spec.Host.OutputMode != "" && spec.Host.OutputMode != "discard" && spec.Host.OutputMode != "private_file" {
			return serviceError("TEMPLATE_HOST_OUTPUT_INVALID", "Host output mode must be discard or private_file.", 400, false, nil)
		}

	case DeploymentContainer:
		if spec.Container == nil || !validImageReference(spec.Container.Image) || spec.Endpoint.ContainerPort < 1 || spec.Endpoint.ContainerPort > 65535 {
			return serviceError("TEMPLATE_CONTAINER_INVALID", "Single-container templates require an image and a valid container Web port.", 400, false, nil)
		}
		if len(spec.Container.Entrypoint) > 1 || len(spec.Container.Command) > 128 || len(spec.Container.Environment) > 256 || len(spec.Container.Mounts) > 128 {
			return serviceError("TEMPLATE_CONTAINER_INVALID", "The single-container command or resource definition exceeds its limit.", 400, false, nil)
		}
		for key, value := range spec.Container.Environment {
			if !templateParameterPattern.MatchString(key) || strings.ContainsAny(value, "\x00\r\n") {
				return serviceError("TEMPLATE_CONTAINER_INVALID", "A container environment variable is invalid.", 400, false, nil)
			}
		}
		for _, mount := range spec.Container.Mounts {
			if (mount.Type != "workspace" && mount.Type != "bind" && mount.Type != "volume" && mount.Type != "tmpfs") || !filepath.IsAbs(mount.Target) ||
				(mount.Type == "bind" && !filepath.IsAbs(mount.Source)) ||
				strings.Contains(strings.ToLower(mount.Target), "docker.sock") || strings.Contains(strings.ToLower(mount.Source), "docker.sock") {
				return serviceError("TEMPLATE_MOUNT_REJECTED", "Container mounts must use absolute targets and cannot expose the container engine socket.", 400, false, nil)
			}
			if mount.Type == "volume" && !managedWorkspaceIdentityPattern.MatchString(strings.TrimSpace(mount.ResourceID)) {
				return serviceError("TEMPLATE_RESOURCE_ID_INVALID", "Managed container volumes require a stable resource identity.", 400, false, nil)
			}
		}
		switch spec.Container.RuntimeProfile {
		case "", ContainerRuntimeProfileRestricted:
		case ContainerRuntimeProfileInteractiveDesktop:
			if spec.Container.ReadOnlyRoot || spec.Container.PIDsLimit != 2048 || strings.TrimSpace(spec.Container.User) != "" || len(spec.Container.Entrypoint) != 0 || (!strings.Contains(spec.Container.Image, "@sha256:") && spec.Container.Image != ArtifactPlaceholder) {
				return serviceError("TEMPLATE_RUNTIME_PROFILE_INVALID", "Interactive desktop templates must use the reviewed writable-root, root-entrypoint, digest-pinned runtime contract.", 400, false, nil)
			}
		default:
			return serviceError("TEMPLATE_RUNTIME_PROFILE_INVALID", "The container runtime profile is not supported.", 400, false, nil)
		}
	case DeploymentCompose:
		if spec.Compose == nil || !composeServicePattern.MatchString(spec.Compose.MainService) || spec.Endpoint.ContainerPort < 1 || spec.Endpoint.ContainerPort > 65535 {
			return serviceError("TEMPLATE_COMPOSE_INVALID", "Compose templates require an entry service and a valid container Web port.", 400, false, nil)
		}
		if err := ValidateComposeYAML(spec.Compose.YAML, spec.Compose.MainService); err != nil {
			return err
		}
	default:
		return serviceError("DEPLOYMENT_INVALID", "Choose a host, single-container, or Compose template.", 400, false, nil)
	}
	return nil
}

func ValidateNPMHostPackage(value NPMHostPackageSpec, parameters []TemplateParameter) error {
	registryURL, err := url.Parse(strings.TrimSpace(value.RegistryURL))
	if err != nil || registryURL.Scheme != "https" || registryURL.Hostname() == "" || registryURL.User != nil || registryURL.RawQuery != "" || registryURL.Fragment != "" {
		return serviceError("NPM_REGISTRY_INVALID", "npm Host templates require an absolute HTTPS Registry URL without credentials, query parameters, or fragments.", 400, false, err)
	}
	validVersion := validExactVersion(value.Version)
	if !npmPackagePattern.MatchString(strings.TrimSpace(value.PackageName)) || !exactSemverPattern.MatchString(strings.TrimSpace(value.Version)) || !validVersion || !npmExecutablePattern.MatchString(strings.TrimSpace(value.Executable)) {
		return serviceError("NPM_PACKAGE_INVALID", "npm Host templates require a valid package name, exact SemVer, and executable name.", 400, false, nil)
	}
	parameterName := strings.TrimSpace(value.AuthTokenParameter)
	if parameterName == "" {
		return nil
	}
	for _, parameter := range parameters {
		if parameter.Name == parameterName && parameter.Type == "secret" {
			return nil
		}
	}
	return serviceError("NPM_AUTH_PARAMETER_INVALID", "The npm auth token must reference a Secret template parameter.", 400, false, nil)
}

func validImageReference(value string) bool {
	value = strings.TrimSpace(value)
	return value != "" && len(value) <= 512 && !strings.ContainsAny(value, "\x00\r\n\t ")
}

func ValidateComposeYAML(raw, mainService string) error {
	if len(raw) == 0 || len(raw) > 512*1024 || strings.ContainsRune(raw, '\x00') {
		return serviceError("TEMPLATE_COMPOSE_INVALID", "Compose YAML is empty or exceeds the 512 KiB limit.", 400, false, nil)
	}
	var document map[string]any
	if err := yaml.Unmarshal([]byte(raw), &document); err != nil {
		return serviceError("TEMPLATE_COMPOSE_INVALID", "Compose YAML could not be parsed.", 400, false, err)
	}
	for _, forbidden := range []string{"include", "secrets", "configs"} {
		if _, ok := document[forbidden]; ok {
			return serviceError("TEMPLATE_COMPOSE_POLICY_REJECTED", "Compose include, secrets, and configs are not supported in managed templates.", 400, false, nil)
		}
	}
	if volumes, ok := document["volumes"].(map[string]any); ok {
		for _, rawVolume := range volumes {
			if rawVolume == nil {
				continue
			}
			volume, ok := rawVolume.(map[string]any)
			if !ok || len(volume) != 0 {
				return serviceError("TEMPLATE_COMPOSE_POLICY_REJECTED", "Managed Compose volumes cannot be external, named, or configured with host driver options.", 400, false, nil)
			}
		}
	}
	if networks, ok := document["networks"].(map[string]any); ok {
		for _, rawNetwork := range networks {
			if network, ok := rawNetwork.(map[string]any); ok && (truthyYAML(network["external"]) || yamlString(network, "name") != "") {
				return serviceError("TEMPLATE_COMPOSE_POLICY_REJECTED", "External or explicitly named Compose networks are not supported in managed templates.", 400, false, nil)
			}
		}
	}
	services, ok := document["services"].(map[string]any)
	if !ok || len(services) == 0 {
		return serviceError("TEMPLATE_COMPOSE_INVALID", "Compose YAML must define services.", 400, false, nil)
	}
	if _, ok := services[mainService]; !ok {
		return serviceError("TEMPLATE_COMPOSE_INVALID", "The Compose entry service does not exist.", 400, false, nil)
	}
	for name, rawService := range services {
		if !composeServicePattern.MatchString(name) {
			return serviceError("TEMPLATE_COMPOSE_INVALID", "A Compose service name is invalid.", 400, false, nil)
		}
		service, ok := rawService.(map[string]any)
		if !ok || !validImageReference(fmt.Sprint(service["image"])) {
			return serviceError("TEMPLATE_COMPOSE_INVALID", "Every Compose service must use a published image.", 400, false, nil)
		}
		for _, forbidden := range []string{
			"build", "ports", "devices", "extends", "container_name", "cap_add", "device_cgroup_rules", "volumes_from",
			"env_file", "label_file", "develop", "use_api_socket", "credential_spec", "uts", "userns_mode", "cgroup",
			"cgroup_parent", "runtime", "profiles", "scale", "deploy",
		} {
			if _, ok := service[forbidden]; ok {
				return serviceError("TEMPLATE_COMPOSE_POLICY_REJECTED", "The Compose service requests a host-level or externally managed capability that Redeven does not allow.", 400, false, nil)
			}
		}
		networkMode := yamlString(service, "network_mode")
		if truthyYAML(service["privileged"]) || (networkMode != "" && networkMode != "bridge" && networkMode != "default" && networkMode != "none") || strings.EqualFold(fmt.Sprint(service["pid"]), "host") || strings.EqualFold(fmt.Sprint(service["ipc"]), "host") {
			return serviceError("TEMPLATE_COMPOSE_POLICY_REJECTED", "Privileged or host namespace Compose services are not allowed.", 400, false, nil)
		}
		if strings.Contains(strings.ToLower(fmt.Sprint(service["volumes"])), "docker.sock") {
			return serviceError("TEMPLATE_COMPOSE_POLICY_REJECTED", "Compose services cannot mount the container engine socket.", 400, false, nil)
		}
		if err := validateComposeMounts(service["volumes"]); err != nil {
			return err
		}
	}
	return nil
}

func validateComposeMounts(raw any) error {
	items, ok := raw.([]any)
	if raw == nil {
		return nil
	}
	if !ok {
		return serviceError("TEMPLATE_COMPOSE_INVALID", "Compose service volumes must be a list.", 400, false, nil)
	}
	for _, item := range items {
		switch value := item.(type) {
		case string:
			source := strings.TrimSpace(strings.SplitN(value, ":", 2)[0])
			if source == "" || source == "${REDEVEN_WORKSPACE}" || composeVolumePattern.MatchString(source) {
				continue
			}
			return serviceError("TEMPLATE_COMPOSE_POLICY_REJECTED", "Compose bind mounts may use only ${REDEVEN_WORKSPACE}; other host paths are not allowed.", 400, false, nil)
		case map[string]any:
			mountType := strings.ToLower(strings.TrimSpace(fmt.Sprint(value["type"])))
			source := strings.TrimSpace(fmt.Sprint(value["source"]))
			if mountType == "bind" && source != "${REDEVEN_WORKSPACE}" {
				return serviceError("TEMPLATE_COMPOSE_POLICY_REJECTED", "Compose bind mounts may use only ${REDEVEN_WORKSPACE}; other host paths are not allowed.", 400, false, nil)
			}
			if mountType == "volume" && source != "" && !composeVolumePattern.MatchString(source) {
				return serviceError("TEMPLATE_COMPOSE_POLICY_REJECTED", "Compose volume mounts must use a project-owned named volume.", 400, false, nil)
			}
			if mountType != "bind" && mountType != "volume" && mountType != "tmpfs" {
				return serviceError("TEMPLATE_COMPOSE_POLICY_REJECTED", "Compose mounts must be project volumes, managed workspace binds, or tmpfs mounts.", 400, false, nil)
			}
		default:
			return serviceError("TEMPLATE_COMPOSE_INVALID", "A Compose service volume definition is invalid.", 400, false, nil)
		}
	}
	return nil
}

func truthyYAML(value any) bool {
	flag, _ := value.(bool)
	return flag
}

func yamlString(value map[string]any, key string) string {
	raw, ok := value[key]
	if !ok || raw == nil {
		return ""
	}
	return strings.TrimSpace(fmt.Sprint(raw))
}

func validExactVersion(value string) bool {
	if !exactSemverPattern.MatchString(value) {
		return false
	}
	core := strings.SplitN(value, "+", 2)[0]
	parts := strings.SplitN(core, "-", 2)
	if len(parts) == 2 {
		for _, id := range strings.Split(parts[1], ".") {
			if id == "" {
				return false
			}
			numeric := true
			for _, c := range id {
				if c < '0' || c > '9' {
					numeric = false
				}
			}
			if numeric && len(id) > 1 && id[0] == '0' {
				return false
			}
		}
	}
	return true
}
