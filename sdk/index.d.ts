// Generated from the published Go contracts. Do not edit.
export interface ComposeTemplateSpec {
  yaml: string;
  main_service: string;
}

export interface ContainerDeviceSpec {
  resource_id?: string;
  host_path: string;
  container_path?: string;
  permissions?: string;
}

export interface ContainerMountSpec {
  resource_id?: string;
  type: string;
  source?: string;
  target: string;
  read_only?: boolean;
  tmpfs_options?: Array<string>;
}

export interface ContainerPortSpec {
  resource_id?: string;
  container_port: number;
  host_port?: number;
  host_ip?: string;
  protocol?: string;
}

export interface ContainerTemplateSpec {
  image: string;
  entrypoint?: Array<string>;
  command?: Array<string>;
  environment?: Record<string, string>;
  labels?: Record<string, string>;
  restart_policy?: string;
  network_mode?: string;
  pid_mode?: string;
  ipc_mode?: string;
  ports?: Array<ContainerPortSpec>;
  mounts?: Array<ContainerMountSpec>;
  cap_add?: Array<string>;
  cap_drop?: Array<string>;
  devices?: Array<ContainerDeviceSpec>;
  privileged?: boolean;
  security_opts?: Array<string>;
  user?: string;
  read_only_root: boolean;
  memory_bytes?: number;
  cpus?: number;
  pids_limit?: number;
  shm_size_bytes?: number;
  runtime_profile?: string;
}

export interface Document {
  kind: string;
  schema_version: number;
  template_id: string;
  service_family_id: string;
  revision: number;
  default_locale: string;
  locales: Array<string>;
  recommended_version?: string;
  sort_order?: number;
  developer_preview?: boolean;
  disk_bytes?: number;
  source_url?: string;
  docker_source_url?: string;
  deployment?: 'host' | 'container' | 'compose';
  container_mode?: string;
  default_access_mode?: string;
  supported_platforms?: Array<string>;
  platform_artifacts?: Record<string, string>;
  release_discovery?: ReleaseDiscovery;
  notices?: Array<Notice>;
  icon?: IconReference;
  spec: Spec;
}

export interface File {
  path: string;
  mode: string;
  content: string;
}

export interface GitSource {
  repository: string;
  ref?: string;
  path?: string;
}

export interface HostArtifactSpec {
  download_url: string;
  size_bytes: number;
  sha256: string;
  executable_rel_path: string;
}

export interface HostTemplateSpec {
  install_script?: string;
  start_script: string;
  stop_script?: string;
  uninstall_script?: string;
  environment?: Record<string, string>;
  artifact?: HostArtifactSpec;
  npm?: NPMHostPackageSpec;
  after_start_script?: string;
  open_script?: string;
  output_mode?: string;
}

export interface IconAsset {
  media_type: string;
  data: string;
  sha256: string;
}

export interface IconReference {
  path: string;
  media_type: string;
}

export interface Localization {
  name: string;
  description: string;
  notices?: Record<string, LocalizedNotice>;
}

export interface LocalizedNotice {
  title: string;
  description: string;
}

export interface NPMHostPackageSpec {
  package_name: string;
  version: string;
  registry_url: string;
  auth_token_parameter?: string;
  executable: string;
}

export interface Notice {
  id: string;
  revision: number;
  severity: string;
  acknowledgement_required: boolean;
}

export interface ReleaseDiscovery {
  source: string;
}

export interface ResolvedSource {
  repository: string;
  repository_id: number;
  ref: string;
  path: string;
  commit_sha: string;
  tree_sha?: string;
}

export interface Snapshot {
  source: ResolvedSource;
  files: Array<File>;
  sha256: string;
}

export interface SourceCatalog {
  source: ResolvedSource;
  templates: Array<SourceEntry>;
}

export interface SourceEntry {
  path: string;
  entrypoint: string;
}

export interface Spec {
  schema_version: number;
  kind: 'host' | 'container' | 'compose';
  endpoint: WebEndpointSpec;
  parameters?: Array<TemplateParameter>;
  host?: HostTemplateSpec;
  container?: ContainerTemplateSpec;
  compose?: ComposeTemplateSpec;
}

export interface TemplateParameter {
  name: string;
  label: string;
  description?: string;
  type: string;
  required?: boolean;
  default?: string;
}

export interface WebEndpointSpec {
  scheme: string;
  container_port?: number;
  fixed_host_port?: number;
  path?: string;
  health_path?: string;
  health_protocol?: string;
  startup_timeout_sec?: number;
}

export const FILENAME: 'redeven-service-template.json';
export const MAX_FILE_BYTES: number;
export const MAX_DIRECTORY_BYTES: number;
export const MAX_FILES: number;
export class TemplateSourceError extends Error { readonly code: string; }
export interface AcquisitionOptions {
  signal?: AbortSignal;
  fetch?: typeof globalThis.fetch;
  onProgress?: (progress: {phase: 'downloading'; files: number; bytes: number}) => void;
}
export function sourceDigest(files: File[]): string;
export function resolveSource(input: GitSource, token?: string, options?: AcquisitionOptions): Promise<ResolvedSource>;
export function discoverSources(input: GitSource, token?: string, options?: AcquisitionOptions): Promise<SourceCatalog>;
export function captureSource(input: GitSource, token?: string, options?: AcquisitionOptions): Promise<Snapshot>;
