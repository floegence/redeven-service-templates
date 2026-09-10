// Package template defines and reads versioned Redeven Service Templates.
package template

type Deployment string

const (
	DeploymentHost                            Deployment = "host"
	DeploymentContainer                       Deployment = "container"
	DeploymentCompose                         Deployment = "compose"
	ContainerRuntimeProfileRestricted                    = "restricted"
	ContainerRuntimeProfileInteractiveDesktop            = "interactive_desktop"
)

type TemplateParameter struct {
	Name        string `json:"name"`
	Label       string `json:"label"`
	Description string `json:"description,omitempty"`
	Type        string `json:"type"`
	Required    bool   `json:"required,omitempty"`
	Default     string `json:"default,omitempty"`
}

type WebEndpointSpec struct {
	Scheme         string `json:"scheme"`
	ContainerPort  int    `json:"container_port,omitempty"`
	FixedHostPort  int    `json:"fixed_host_port,omitempty"`
	Path           string `json:"path,omitempty"`
	HealthPath     string `json:"health_path,omitempty"`
	HealthProtocol string `json:"health_protocol,omitempty"`
	StartupTimeout int    `json:"startup_timeout_sec,omitempty"`
}

type HostArtifactSpec struct {
	DownloadURL       string `json:"download_url"`
	SizeBytes         int64  `json:"size_bytes"`
	SHA256            string `json:"sha256"`
	ExecutableRelPath string `json:"executable_rel_path"`
}

type NPMHostPackageSpec struct {
	PackageName        string `json:"package_name"`
	Version            string `json:"version"`
	RegistryURL        string `json:"registry_url"`
	AuthTokenParameter string `json:"auth_token_parameter,omitempty"`
	Executable         string `json:"executable"`
}

type HostTemplateSpec struct {
	InstallScript    string              `json:"install_script,omitempty"`
	StartScript      string              `json:"start_script"`
	StopScript       string              `json:"stop_script,omitempty"`
	UninstallScript  string              `json:"uninstall_script,omitempty"`
	Environment      map[string]string   `json:"environment,omitempty"`
	Artifact         *HostArtifactSpec   `json:"artifact,omitempty"`
	NPM              *NPMHostPackageSpec `json:"npm,omitempty"`
	AfterStartScript string              `json:"after_start_script,omitempty"`
	OpenScript       string              `json:"open_script,omitempty"`
	OutputMode       string              `json:"output_mode,omitempty"`
}

type ContainerMountSpec struct {
	ResourceID   string   `json:"resource_id,omitempty"`
	Type         string   `json:"type"`
	Source       string   `json:"source,omitempty"`
	Target       string   `json:"target"`
	ReadOnly     bool     `json:"read_only,omitempty"`
	TmpfsOptions []string `json:"tmpfs_options,omitempty"`
}

type ContainerPortSpec struct {
	ResourceID    string `json:"resource_id,omitempty"`
	ContainerPort int    `json:"container_port"`
	HostPort      int    `json:"host_port,omitempty"`
	HostIP        string `json:"host_ip,omitempty"`
	Protocol      string `json:"protocol,omitempty"`
}

type ContainerDeviceSpec struct {
	ResourceID    string `json:"resource_id,omitempty"`
	HostPath      string `json:"host_path"`
	ContainerPath string `json:"container_path,omitempty"`
	Permissions   string `json:"permissions,omitempty"`
}

type ContainerTemplateSpec struct {
	Image          string                `json:"image"`
	Entrypoint     []string              `json:"entrypoint,omitempty"`
	Command        []string              `json:"command,omitempty"`
	Environment    map[string]string     `json:"environment,omitempty"`
	Labels         map[string]string     `json:"labels,omitempty"`
	RestartPolicy  string                `json:"restart_policy,omitempty"`
	NetworkMode    string                `json:"network_mode,omitempty"`
	PIDMode        string                `json:"pid_mode,omitempty"`
	IPCMode        string                `json:"ipc_mode,omitempty"`
	Ports          []ContainerPortSpec   `json:"ports,omitempty"`
	Mounts         []ContainerMountSpec  `json:"mounts,omitempty"`
	CapAdd         []string              `json:"cap_add,omitempty"`
	CapDrop        []string              `json:"cap_drop,omitempty"`
	Devices        []ContainerDeviceSpec `json:"devices,omitempty"`
	Privileged     bool                  `json:"privileged,omitempty"`
	SecurityOpts   []string              `json:"security_opts,omitempty"`
	User           string                `json:"user,omitempty"`
	ReadOnlyRoot   bool                  `json:"read_only_root"`
	MemoryBytes    int64                 `json:"memory_bytes,omitempty"`
	CPUs           float64               `json:"cpus,omitempty"`
	PIDsLimit      int64                 `json:"pids_limit,omitempty"`
	ShmSizeBytes   int64                 `json:"shm_size_bytes,omitempty"`
	RuntimeProfile string                `json:"runtime_profile,omitempty"`
}

type ComposeTemplateSpec struct {
	YAML        string `json:"yaml"`
	MainService string `json:"main_service"`
}

type Spec struct {
	SchemaVersion int                    `json:"schema_version"`
	Kind          Deployment             `json:"kind"`
	Endpoint      WebEndpointSpec        `json:"endpoint"`
	Parameters    []TemplateParameter    `json:"parameters,omitempty"`
	Host          *HostTemplateSpec      `json:"host,omitempty"`
	Container     *ContainerTemplateSpec `json:"container,omitempty"`
	Compose       *ComposeTemplateSpec   `json:"compose,omitempty"`
}
