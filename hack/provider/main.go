package main

import (
	"bufio"
	"fmt"
	"maps"
	"net/url"
	"os"
	"path/filepath"
	"strings"

	"github.com/goccy/go-yaml"
)

const (
	providerName = "kubernetes"
	githubOwner  = "skevetter"
	githubRepo   = "devpod-provider-kubernetes"
)

type Provider struct {
	Name         string            `yaml:"name"`
	Version      string            `yaml:"version"`
	Icon         string            `yaml:"icon"`
	Home         string            `yaml:"home"`
	Description  string            `yaml:"description"`
	OptionGroups []OptionGroup     `yaml:"optionGroups"`
	Options      Options           `yaml:"options"`
	Agent        Agent             `yaml:"agent"`
	Exec         map[string]string `yaml:"exec"`
}

type OptionGroup struct {
	Name           string   `yaml:"name"`
	Options        []string `yaml:"options"`
	DefaultVisible bool     `yaml:"defaultVisible,omitempty"`
}

type Options map[string]Option

type Option struct {
	Description string `yaml:"description,omitempty"`
	Default     string `yaml:"default,omitempty"`
	Command     string `yaml:"command,omitempty"`
	Type        string `yaml:"type,omitempty"`
	Global      bool   `yaml:"global,omitempty"`
}

type Agent struct {
	ContainerInactivityTimeout string         `yaml:"containerInactivityTimeout"`
	Local                      bool           `yaml:"local"`
	Dockerless                 Dockerless     `yaml:"dockerless"`
	Binaries                   map[string]any `yaml:"binaries"`
	Driver                     string         `yaml:"driver"`
	Custom                     CustomDriver   `yaml:"custom"`
}

type Dockerless struct {
	Disabled string `yaml:"disabled"`
	Image    string `yaml:"image"`
}

type CustomDriver struct {
	FindDevContainer    string `yaml:"findDevContainer"`
	CommandDevContainer string `yaml:"commandDevContainer"`
	StartDevContainer   string `yaml:"startDevContainer"`
	StopDevContainer    string `yaml:"stopDevContainer"`
	RunDevContainer     string `yaml:"runDevContainer"`
	DeleteDevContainer  string `yaml:"deleteDevContainer"`
	TargetArchitecture  string `yaml:"targetArchitecture"`
	CanReprovision      bool   `yaml:"canReprovision"`
}

type Binary struct {
	OS       string `yaml:"os"`
	Arch     string `yaml:"arch"`
	Path     string `yaml:"path"`
	Checksum string `yaml:"checksum"`
}

type buildConfig struct {
	version     string
	projectRoot string
	isRelease   bool
	checksums   map[string]string
}

func main() {
	if err := run(); err != nil {
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		os.Exit(1)
	}
}

func run() error {
	if len(os.Args) != 2 {
		return fmt.Errorf("expected version as argument")
	}

	cfg, err := newBuildConfig(os.Args[1])
	if err != nil {
		return err
	}

	provider := buildProvider(cfg)

	output, err := yaml.Marshal(provider)
	if err != nil {
		return fmt.Errorf("marshal yaml: %w", err)
	}

	_, err = os.Stdout.Write(output)
	return err
}

func newBuildConfig(version string) (*buildConfig, error) {
	checksums, err := parseChecksums("./dist/checksums.txt")
	if err != nil {
		return nil, fmt.Errorf("parse checksums: %w", err)
	}

	projectRoot := os.Getenv("PROJECT_ROOT")
	if projectRoot == "" {
		owner := getEnvOrDefault("GITHUB_OWNER", githubOwner)
		projectRoot = fmt.Sprintf(
			"https://github.com/%s/%s/releases/download/%s",
			owner,
			githubRepo,
			version,
		)
	}

	isRelease := strings.Contains(projectRoot, "github.com") &&
		strings.Contains(projectRoot, "/releases/")

	return &buildConfig{
		version:     version,
		projectRoot: projectRoot,
		isRelease:   isRelease,
		checksums:   checksums,
	}, nil
}

func buildProvider(cfg *buildConfig) Provider {
	return Provider{
		Name:         providerName,
		Version:      cfg.version,
		Icon:         "https://devpod.sh/assets/kubernetes.svg",
		Home:         "https://github.com/skevetter/devpod",
		Description:  "DevPod on Kubernetes",
		OptionGroups: buildOptionGroups(),
		Options:      buildOptions(),
		Agent:        buildAgent(cfg),
		Exec: map[string]string{
			"command": "\"${DEVPOD}\" helper sh -c \"${COMMAND}\"",
		},
	}
}

func buildOptionGroups() []OptionGroup {
	return []OptionGroup{
		{
			Name:           "Options",
			DefaultVisible: true,
			Options:        []string{"KUBERNETES_NAMESPACE", "DISK_SIZE"},
		},
		{
			Name:    "Kubernetes Config",
			Options: []string{"KUBERNETES_CONTEXT", "KUBERNETES_CONFIG"},
		},
		{
			Name: "Advanced Options",
			Options: []string{
				"CLUSTER_ROLE", "SERVICE_ACCOUNT", "CREATE_NAMESPACE",
				"KUBECTL_PATH", "INACTIVITY_TIMEOUT", "STORAGE_CLASS",
				"PVC_ACCESS_MODE", "PVC_ANNOTATIONS", "RESOURCES",
				"POD_MANIFEST_TEMPLATE", "ARCH_DETECTION_POD_MANIFEST_TEMPLATE",
				"NODE_SELECTOR", "HELPER_RESOURCES", "HELPER_IMAGE",
				"LABELS", "DOCKERLESS_DISABLED", "DOCKERLESS_IMAGE",
			},
		},
	}
}

func buildOptions() Options {
	opts := Options{}
	maps.Copy(opts, buildCoreOptions())
	maps.Copy(opts, buildK8sOptions())
	maps.Copy(opts, buildAdvancedOptions())
	maps.Copy(opts, buildDockerlessOptions())
	return opts
}

func buildCoreOptions() Options {
	return Options{
		"DISK_SIZE": {
			Description: "The default size for the persistent volume to use.",
			Default:     "10Gi",
			Global:      true,
		},
		"KUBERNETES_CONTEXT": {
			Description: "The kubernetes context to use. E.g. my-kube-context",
		},
		"KUBERNETES_CONFIG": {
			Description: "The kubernetes config to use. E.g. /path/to/my/kube/config.yaml",
		},
		"KUBERNETES_PULL_SECRETS_ENABLED": {
			Description: "If true, DevPod will try to use the pull secrets from the current context.",
			Default:     "true",
			Type:        "boolean",
			Global:      true,
		},
		"KUBERNETES_NAMESPACE": {
			Description: "The kubernetes namespace to use",
			Command:     namespaceCommand(),
		},
	}
}

func namespaceCommand() string {
	return `NAMESPACE=$(${KUBECTL_PATH} config view --kubeconfig=${KUBERNETES_CONFIG}` +
		` --context=${KUBERNETES_CONTEXT} --minify -o jsonpath='{..namespace}' 2>/dev/null || true)
if [ -z "${NAMESPACE}" ]; then
  NAMESPACE=devpod
fi
echo $NAMESPACE`
}

func buildK8sOptions() Options {
	return Options{
		"CREATE_NAMESPACE": {
			Description: "If true, DevPod will try to create the namespace.",
			Default:     "true",
			Type:        "boolean",
			Global:      true,
		},
		"CLUSTER_ROLE": {
			Description: "If defined, DevPod will create a role binding for the given cluster role.",
			Global:      true,
		},
		"SERVICE_ACCOUNT": {
			Description: "If defined, DevPod will use the given service account for the dev container.",
			Global:      true,
		},
		"HELPER_IMAGE": {
			Description: "The image DevPod will use to find out the cluster architecture. Defaults to alpine.",
			Global:      true,
		},
		"HELPER_RESOURCES": {
			Description: "The resources to use for the workspace init container. E.g. requests.cpu=100m,limits.memory=1Gi",
			Global:      true,
		},
		"KUBECTL_PATH": {
			Description: "The path where to find the kubectl binary.",
			Default:     "kubectl",
			Global:      true,
		},
		"INACTIVITY_TIMEOUT": {
			Description: "If defined, will automatically stop the pod after the inactivity period. Examples: 10m, 1h",
		},
		"POD_TIMEOUT": {
			Description: "Determines how long the provider waits for the workspace pod to come up. Examples: 10m, 1h",
			Default:     "10m",
		},
		"STORAGE_CLASS": {
			Description: "If defined, DevPod will use the given storage class to create the persistent volume claim. " +
				"You will need to ensure the storage class exists in your cluster!",
			Global: true,
		},
	}
}

func buildAdvancedOptions() Options {
	return Options{
		"PVC_ACCESS_MODE": {
			Description: "If defined, DevPod will use the given access mode to create the persistent volume claim. " +
				"You will need to ensure the storage class support the given access mode!. E.g. RWO or ROX or RWX or RWOP",
			Global: true,
		},
		"PVC_ANNOTATIONS": {
			Description: "If defined, DevPod will use add the given annotations to the main workspace pvc",
			Global:      true,
		},
		"NODE_SELECTOR": {
			Description: "The node selector to use for the workspace pod. E.g. my-label=value,my-label-2=value-2",
			Global:      true,
		},
		"RESOURCES": {
			Description: "The resources to use for the workspace container. " +
				"E.g. requests.cpu=500m,limits.memory=5Gi,limits.gpu-vendor.example/example-gpu=1",
			Global: true,
		},
		"POD_MANIFEST_TEMPLATE": {
			Description: "Pod manifest template file path used as template to build the devpod pod. " +
				"E.g. /path/pod_manifest.yaml. Alternatively can be an inline yaml string.",
			Global: true,
			Type:   "multiline",
		},
		"ARCH_DETECTION_POD_MANIFEST_TEMPLATE": {
			Description: "Pod manifest template file path used as template to build the architecture detection pod. " +
				"E.g. /path/pod_manifest.yaml. Alternatively can be an inline yaml string.",
			Global: true,
			Type:   "multiline",
		},
		"LABELS": {
			Description: "The labels to use for the workspace pod. E.g. devpod.sh/example=value,devpod.sh/example2=value2",
			Global:      true,
		},
		"DANGEROUSLY_OVERRIDE_IMAGE": {
			Description: "Only set this if you know what you're doing! " +
				"Overrides the pod base image and could break your workspace.",
			Global:  true,
			Default: "",
		},
		"STRICT_SECURITY": {
			Description: "EXPERIMENTAL! Use at your own risk. Removes the default security context " +
				"and merges the one from POD_MANIFEST_TEMPLATE if specified.",
			Type:    "boolean",
			Default: "false",
		},
		"WORKSPACE_VOLUME_MOUNT": {
			Description: "Sets the path of the workspace volume mount. " +
				"By default it is the root of your workspace source code, " +
				"usually /workspaces/$WORKSPACE_ID. " +
				"If you intend to create multi-repo workspaces or need additional files " +
				"throughout the lifecycle of the workspace, " +
				"set this option to a parent directory of the workspace mount.",
			Type: "string",
		},
	}
}

func buildDockerlessOptions() Options {
	return Options{
		"DOCKERLESS_IMAGE": {
			Description: "The dockerless image to use.",
			Global:      true,
		},
		"DOCKERLESS_DISABLED": {
			Description: "If dockerless should be disabled. " +
				"Dockerless is the way DevPod uses to build images directly " +
				"within Kubernetes. If dockerless is disabled and no image is specified, " +
				"DevPod will fail instead.",
			Global:  true,
			Default: "false",
		},
	}
}

func buildAgent(cfg *buildConfig) Agent {
	return Agent{
		ContainerInactivityTimeout: "${INACTIVITY_TIMEOUT}",
		Local:                      true,
		Dockerless: Dockerless{
			Disabled: "${DOCKERLESS_DISABLED}",
			Image:    "${DOCKERLESS_IMAGE}",
		},
		Binaries: map[string]any{
			"KUBERNETES_PROVIDER": buildBinaryList(cfg, allPlatforms()),
		},
		Driver: "custom",
		Custom: CustomDriver{
			FindDevContainer:    "\"${KUBERNETES_PROVIDER}\" find",
			CommandDevContainer: "\"${KUBERNETES_PROVIDER}\" command",
			StartDevContainer:   "\"${KUBERNETES_PROVIDER}\" start",
			StopDevContainer:    "\"${KUBERNETES_PROVIDER}\" stop",
			RunDevContainer:     "\"${KUBERNETES_PROVIDER}\" run",
			DeleteDevContainer:  "\"${KUBERNETES_PROVIDER}\" delete",
			TargetArchitecture:  "\"${KUBERNETES_PROVIDER}\" target-architecture",
			CanReprovision:      true,
		},
	}
}

func buildBinaryList(cfg *buildConfig, platforms []string) []Binary {
	result := make([]Binary, 0, len(platforms))
	for _, platform := range platforms {
		result = append(result, buildBinary(cfg, platform))
	}
	return result
}

func buildBinary(cfg *buildConfig, platform string) Binary {
	os, arch, _ := strings.Cut(platform, "/")

	path := cfg.projectRoot
	if !cfg.isRelease {
		if strings.HasPrefix(path, "http://") || strings.HasPrefix(path, "https://") {
			base, _ := url.Parse(path)
			joined, _ := url.JoinPath(base.String(), buildDir(platform))
			path = joined
		} else {
			absPath, _ := filepath.Abs(path)
			path = filepath.Join(absPath, buildDir(platform))
		}
	}

	filename := fmt.Sprintf("devpod-provider-%s-%s-%s", providerName, os, arch)
	if os == "windows" {
		filename += ".exe"
	}

	if strings.HasPrefix(path, "http://") || strings.HasPrefix(path, "https://") {
		path, _ = url.JoinPath(path, filename)
	} else {
		path = filepath.Join(path, filename)
	}

	return Binary{
		OS:       os,
		Arch:     arch,
		Path:     path,
		Checksum: cfg.checksums[filename],
	}
}

func buildDir(platform string) string {
	dirs := map[string]string{
		"linux/amd64":   "build_linux_amd64_v1",
		"linux/arm64":   "build_linux_arm64_v8.0",
		"darwin/amd64":  "build_darwin_amd64_v1",
		"darwin/arm64":  "build_darwin_arm64_v8.0",
		"windows/amd64": "build_windows_amd64_v1",
	}
	return dirs[platform]
}

func allPlatforms() []string {
	return []string{"linux/amd64", "linux/arm64", "darwin/amd64", "darwin/arm64", "windows/amd64"}
}

func parseChecksums(path string) (map[string]string, error) {
	file, err := os.Open(path) //nolint:gosec // path is a build-time constant, not user input
	if err != nil {
		return nil, err
	}
	defer func() { _ = file.Close() }()

	checksums := make(map[string]string)
	scanner := bufio.NewScanner(file)
	for scanner.Scan() {
		if checksum, filename, ok := strings.Cut(scanner.Text(), "  "); ok {
			checksums[strings.TrimSpace(filename)] = checksum
		}
	}

	return checksums, scanner.Err()
}

func getEnvOrDefault(key, defaultValue string) string {
	if value := os.Getenv(key); value != "" {
		return value
	}
	return defaultValue
}
