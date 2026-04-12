package kubernetes

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"maps"
	"path/filepath"
	"strconv"
	"strings"

	optionspkg "github.com/skevetter/devpod-provider-kubernetes/pkg/options"
	"github.com/skevetter/devpod/pkg/devcontainer/config"
	"github.com/skevetter/devpod/pkg/driver"
	"github.com/skevetter/log"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	_ "k8s.io/client-go/plugin/pkg/client/auth"
)

const (
	DevContainerName  = "devpod"
	InitContainerName = "devpod-init"

	trueStr    = "true"
	volumeType = "volume"
)

const (
	DevPodCreatedLabel      = "devpod.sh/created"
	DevPodWorkspaceLabel    = "devpod.sh/workspace"
	DevPodWorkspaceUIDLabel = "devpod.sh/workspace-uid"

	DevPodInfoAnnotation        = "devpod.sh/info"
	DevPodLastAppliedAnnotation = "devpod.sh/last-applied-configuration"
)

var ExtraDevPodLabels = map[string]string{
	DevPodCreatedLabel: trueStr,
}

type DevContainerInfo struct {
	WorkspaceID string
	Options     *driver.RunOptions
}

func (k *KubernetesDriver) RunDevContainer(
	ctx context.Context,
	workspaceId string,
	options *driver.RunOptions,
) error {
	workspaceId = getID(workspaceId)

	k.ensureNamespace(ctx)

	initialize, options, err := k.ensurePVC(ctx, workspaceId, options)
	if err != nil {
		return err
	}

	return k.runContainer(ctx, workspaceId, options, initialize)
}

func (k *KubernetesDriver) ensurePVC(
	ctx context.Context,
	workspaceId string,
	options *driver.RunOptions,
) (bool, *driver.RunOptions, error) {
	pvc, containerInfo, err := k.getDevContainerPvc(ctx, workspaceId)
	if err != nil {
		return false, nil, err
	}

	if pvc == nil {
		if options == nil {
			return false, nil, fmt.Errorf(
				"no options provided and no persistent volume claim found for workspace '%s'",
				workspaceId,
			)
		}
		err = k.createPersistentVolumeClaim(ctx, workspaceId, options)
		if err != nil {
			return false, nil, err
		}
		return true, options, nil
	}

	if options == nil {
		options = resolveOptionsFromPVC(containerInfo)
	}
	if options == nil {
		return false, nil, fmt.Errorf(
			"workspace '%s' has a PVC but no run options could be resolved",
			workspaceId,
		)
	}
	return false, options, nil
}

func resolveOptionsFromPVC(info *DevContainerInfo) *driver.RunOptions {
	if info != nil && info.Options != nil {
		return info.Options
	}
	return nil
}

func (k *KubernetesDriver) runContainer(
	ctx context.Context,
	id string,
	options *driver.RunOptions,
	initialize bool,
) (err error) {
	mount, err := k.resolveWorkspaceMount(options)
	if err != nil {
		return err
	}
	pod, err := k.loadPodTemplate()
	if err != nil {
		return err
	}

	initContainers := k.getInitContainers(options, pod, initialize)
	volumeMounts := k.buildVolumeMounts(mount, options)
	capabilities := buildCapabilities(options.CapAdd)
	envVars := buildEnvVars(options.Env)

	serviceAccount, err := k.setupServiceAccount(ctx, id)
	if err != nil {
		return err
	}
	meta, err := k.resolveMetadata(pod, options.UID)
	if err != nil {
		return err
	}
	if err = k.ensurePullSecrets(ctx, id, options.Image); err != nil {
		return err
	}
	pod.Name = id
	pod.Labels = meta.labels
	if serviceAccount != "" {
		pod.Spec.ServiceAccountName = serviceAccount
	}
	pod.Spec.NodeSelector = meta.nodeSelector
	pod.Spec.InitContainers = initContainers
	pod.Spec.Containers = getContainers(pod, containerConfig{
		imageName:      options.Image,
		entrypoint:     options.Entrypoint,
		args:           options.Cmd,
		envVars:        envVars,
		volumeMounts:   volumeMounts,
		capabilities:   capabilities,
		resources:      meta.resources,
		privileged:     options.Privileged,
		overrideImage:  k.options.DangerouslyOverrideImage,
		strictSecurity: k.options.StrictSecurity,
	})
	pod.Spec.Volumes = getVolumes(pod, id)

	affinity := k.setupPodAffinity(ctx, pod, id)

	if k.options.KubernetesPullSecretsEnabled == trueStr {
		pod.Spec.ImagePullSecrets = []corev1.LocalObjectReference{
			{Name: getPullSecretsName(id)},
		}
	}

	return k.applyPod(ctx, id, pod, affinity)
}

func (k *KubernetesDriver) resolveWorkspaceMount(
	options *driver.RunOptions,
) (*config.Mount, error) {
	mount := options.WorkspaceMount
	if mount.Target == "" {
		return nil, fmt.Errorf("workspace mount target is empty")
	}
	if k.options.WorkspaceVolumeMount != "" {
		rel, err := filepath.Rel(k.options.WorkspaceVolumeMount, mount.Target)

		switch {
		case err != nil:
			k.Log.Warn("Relative filepath: %v", err)
		case strings.HasPrefix(rel, ".."):
			k.Log.Warnf(
				"Workspace volume mount needs to be the same as the workspace mount or a parent, "+
					"skipping option. WorkspaceVolumeMount: %s, MountTarget: %s",
				k.options.WorkspaceVolumeMount,
				mount.Target,
			)
		default:
			mount.Target = k.options.WorkspaceVolumeMount
			k.Log.Debugf("Using workspace volume mount: %s", k.options.WorkspaceVolumeMount)
		}
	}
	return mount, nil
}

type podMetadata struct {
	labels       map[string]string
	nodeSelector map[string]string
	resources    corev1.ResourceRequirements
}

func (k *KubernetesDriver) resolveMetadata(
	pod *corev1.Pod,
	uid string,
) (podMetadata, error) {
	labels, err := getLabels(pod, k.options.Labels)
	if err != nil {
		return podMetadata{}, err
	}
	labels[DevPodWorkspaceUIDLabel] = uid

	nodeSelector, err := getNodeSelector(pod, k.options.NodeSelector)
	if err != nil {
		return podMetadata{}, err
	}

	resources := resolveResources(pod, k.options.Resources, k.Log)

	return podMetadata{
		labels:       labels,
		nodeSelector: nodeSelector,
		resources:    resources,
	}, nil
}

func (k *KubernetesDriver) loadPodTemplate() (*corev1.Pod, error) {
	pod := &corev1.Pod{
		TypeMeta: metav1.TypeMeta{
			Kind:       "Pod",
			APIVersion: corev1.SchemeGroupVersion.String(),
		},
		Spec: corev1.PodSpec{
			RestartPolicy: corev1.RestartPolicyNever,
		},
	}
	if len(k.options.PodManifestTemplate) > 0 {
		k.Log.Debugf("trying to get pod template manifest from %s", k.options.PodManifestTemplate)
		return getPodTemplate(k.options.PodManifestTemplate)
	}
	return pod, nil
}

func (k *KubernetesDriver) buildVolumeMounts(
	mount *config.Mount,
	options *driver.RunOptions,
) []corev1.VolumeMount {
	volumeMounts := []corev1.VolumeMount{getVolumeMount(0, mount)}
	for idx, m := range options.Mounts {
		switch m.Type {
		case "bind", volumeType:
			volumeMounts = append(volumeMounts, getVolumeMount(idx+1, m))
		default:
			k.Log.Warnf(
				"Unsupported mount type '%s' in mount '%s', will skip",
				m.Type,
				m.String(),
			)
		}
	}
	return volumeMounts
}

func buildCapabilities(capAdd []string) *corev1.Capabilities {
	if len(capAdd) == 0 {
		return nil
	}
	capabilities := &corev1.Capabilities{}
	for _, cap := range capAdd {
		capabilities.Add = append(capabilities.Add, corev1.Capability(cap))
	}
	return capabilities
}

func buildEnvVars(env map[string]string) []corev1.EnvVar {
	envVars := []corev1.EnvVar{}
	for k, v := range env {
		envVars = append(envVars, corev1.EnvVar{Name: k, Value: v})
	}
	return envVars
}

func (k *KubernetesDriver) setupServiceAccount(
	ctx context.Context,
	id string,
) (string, error) {
	if k.options.ServiceAccount == "" {
		return "", nil
	}
	err := k.createServiceAccount(ctx, id, k.options.ServiceAccount)
	if err != nil {
		return "", fmt.Errorf("create service account: %w", err)
	}
	return k.options.ServiceAccount, nil
}

func resolveResources(
	pod *corev1.Pod,
	resourceStr string,
	log log.Logger,
) corev1.ResourceRequirements {
	resources := corev1.ResourceRequirements{}
	if len(pod.Spec.Containers) > 0 {
		resources = pod.Spec.Containers[0].Resources
	}
	if resourceStr != "" {
		resources = parseResources(resourceStr, log)
	}
	return resources
}

func (k *KubernetesDriver) ensurePullSecrets(
	ctx context.Context,
	id string,
	image string,
) error {
	if k.options.KubernetesPullSecretsEnabled != trueStr {
		return nil
	}
	_, err := k.EnsurePullSecret(ctx, getPullSecretsName(id), image)
	return err
}

func (k *KubernetesDriver) setupPodAffinity(
	ctx context.Context,
	pod *corev1.Pod,
	id string,
) bool {
	stdout := &bytes.Buffer{}
	stderr := &bytes.Buffer{}

	err := k.runCommand(
		ctx,
		[]string{"get", "pods", "-o=name", "-l", DevPodWorkspaceLabel + "=" + id},
		cmdIO{stdout: stdout, stderr: stderr},
	)
	if err != nil {
		k.Log.Debugf(
			"skipping finding cluster architecture: %s %s %w",
			stdout.String(),
			stderr.String(),
			err,
		)
	}

	if stdout.String() == "" {
		return false
	}

	affinityPodID := strings.TrimSpace(stdout.String())

	if k.options.NodeSelector != "" {
		return true
	}

	k.Log.Infof("Found architecture detecting pod: %s, using PodAffinity...", affinityPodID)

	if pod.Spec.Affinity == nil {
		pod.Spec.Affinity = &corev1.Affinity{}
	}
	if pod.Spec.Affinity.PodAffinity == nil {
		pod.Spec.Affinity.PodAffinity = &corev1.PodAffinity{
			RequiredDuringSchedulingIgnoredDuringExecution: []corev1.PodAffinityTerm{},
		}
	}

	pod.Spec.Affinity.PodAffinity.RequiredDuringSchedulingIgnoredDuringExecution = append(
		pod.Spec.Affinity.PodAffinity.RequiredDuringSchedulingIgnoredDuringExecution,
		corev1.PodAffinityTerm{
			LabelSelector: &metav1.LabelSelector{
				MatchExpressions: []metav1.LabelSelectorRequirement{
					{
						Key:      DevPodWorkspaceLabel,
						Operator: metav1.LabelSelectorOpIn,
						Values:   []string{id},
					},
				},
			},
			Namespaces:  []string{k.namespace},
			TopologyKey: "kubernetes.io/hostname",
		})

	return true
}

func (k *KubernetesDriver) checkExistingPod(
	ctx context.Context,
	id string,
) (skip bool, err error) {
	existingPod, err := k.getPod(ctx, id)
	if err != nil {
		return false, fmt.Errorf("get pod: %s: %w", id, err)
	}

	if existingPod == nil {
		return false, nil
	}

	existingOptions := &optionspkg.Options{}
	err = json.Unmarshal(
		[]byte(existingPod.GetAnnotations()[DevPodLastAppliedAnnotation]),
		existingOptions,
	)
	if err != nil {
		k.Log.Errorf("Error unmarshalling existing provider options, continuing...: %s", err)
	}

	if optionspkg.Equal(&existingOptions.ComparableOptions, &k.options.ComparableOptions) {
		k.Log.Debug("Provider options did not change, skipping update")
		return true, nil
	}

	k.Log.Debug("Provider options changed")
	err = k.waitPodDeleted(ctx, id)
	if err != nil {
		return false, fmt.Errorf("stop devcontainer: %s: %w", id, err)
	}
	return false, nil
}

func (k *KubernetesDriver) applyPod(
	ctx context.Context,
	id string,
	pod *corev1.Pod,
	affinity bool,
) error {
	skip, err := k.checkExistingPod(ctx, id)
	if err != nil {
		return err
	}
	if skip {
		return nil
	}
	return k.runPod(ctx, id, pod, affinity)
}

func (k *KubernetesDriver) runPod(
	ctx context.Context,
	id string,
	pod *corev1.Pod,
	affinity bool,
) error {
	var err error

	// set configuration before creating the pod
	lastAppliedConfigRaw, err := json.Marshal(k.options)
	if err != nil {
		return fmt.Errorf("marshal last applied config: %w", err)
	}

	if pod.Annotations == nil {
		pod.Annotations = map[string]string{}
	}
	pod.Annotations[DevPodLastAppliedAnnotation] = string(lastAppliedConfigRaw)

	// marshal the pod
	podRaw, err := json.Marshal(pod)
	if err != nil {
		return err
	}

	k.Log.Debugf("Create pod with: %s", string(podRaw))
	// create the pod
	k.Log.Infof("Create Pod '%s'", id)
	buf := &bytes.Buffer{}
	err = k.runCommand(
		ctx,
		[]string{"create", "-f", "-"},
		cmdIO{stdin: strings.NewReader(string(podRaw)), stdout: buf, stderr: buf},
	)
	if err != nil {
		return fmt.Errorf("create pod: %s: %w", buf.String(), err)
	}

	// wait for pod running
	k.Log.Infof("Waiting for DevContainer Pod '%s' to come up...", id)
	err = k.waitPodRunning(ctx, id)
	if err != nil {
		return err
	}

	if affinity {
		k.Log.Infof("Cleaning up architecture detection pod")
		err := k.runCommand(
			ctx,
			[]string{"delete", "pods", "--force", "-l", DevPodWorkspaceLabel + "=" + id},
			cmdIO{stdout: buf, stderr: buf},
		)
		if err != nil {
			return fmt.Errorf("cleanup jobs: %s: %w", buf.String(), err)
		}
	}

	return nil
}

type containerConfig struct {
	imageName      string
	entrypoint     string
	args           []string
	envVars        []corev1.EnvVar
	volumeMounts   []corev1.VolumeMount
	capabilities   *corev1.Capabilities
	resources      corev1.ResourceRequirements
	privileged     *bool
	overrideImage  string
	strictSecurity bool
}

func getContainers(
	pod *corev1.Pod,
	cfg containerConfig,
) []corev1.Container {
	devPodContainer := corev1.Container{
		Name:         DevContainerName,
		Image:        cfg.imageName,
		Command:      []string{cfg.entrypoint},
		Args:         cfg.args,
		Env:          cfg.envVars,
		Resources:    cfg.resources,
		VolumeMounts: cfg.volumeMounts,
		SecurityContext: &corev1.SecurityContext{
			Capabilities: cfg.capabilities,
			Privileged:   cfg.privileged,
			RunAsUser:    &[]int64{0}[0],
			RunAsGroup:   &[]int64{0}[0],
			RunAsNonRoot: &[]bool{false}[0],
		},
	}

	if cfg.overrideImage != "" {
		devPodContainer.Image = cfg.overrideImage
	}

	if cfg.strictSecurity {
		devPodContainer.SecurityContext = nil
	}

	retContainers, existingDevPodContainer := splitExistingContainer(pod)

	if existingDevPodContainer != nil {
		mergeExistingContainer(&devPodContainer, existingDevPodContainer)
	}
	retContainers = append(retContainers, devPodContainer)

	return retContainers
}

func splitExistingContainer(pod *corev1.Pod) ([]corev1.Container, *corev1.Container) {
	retContainers := []corev1.Container{}
	var existing *corev1.Container
	if pod != nil {
		for i, container := range pod.Spec.Containers {
			if container.Name == DevContainerName {
				existing = &pod.Spec.Containers[i]
			} else {
				retContainers = append(retContainers, container)
			}
		}
	}
	return retContainers, existing
}

func mergeExistingContainer(devPodContainer, existing *corev1.Container) {
	devPodContainer.Env = append(existing.Env, devPodContainer.Env...)
	devPodContainer.EnvFrom = existing.EnvFrom
	devPodContainer.Ports = existing.Ports
	devPodContainer.VolumeMounts = append(
		existing.VolumeMounts,
		devPodContainer.VolumeMounts...)
	devPodContainer.ImagePullPolicy = existing.ImagePullPolicy

	if devPodContainer.SecurityContext == nil && existing.SecurityContext != nil {
		devPodContainer.SecurityContext = existing.SecurityContext
	}
}

func getVolumes(pod *corev1.Pod, id string) []corev1.Volume {
	volumes := []corev1.Volume{
		{
			Name: "devpod",
			VolumeSource: corev1.VolumeSource{
				PersistentVolumeClaim: &corev1.PersistentVolumeClaimVolumeSource{
					ClaimName: id,
				},
			},
		},
	}

	if pod.Spec.Volumes != nil {
		volumes = append(volumes, pod.Spec.Volumes...)
	}

	return volumes
}

func getVolumeMount(idx int, mount *config.Mount) corev1.VolumeMount {
	subPath := strconv.Itoa(idx)
	if mount.Type == volumeType && mount.Source != "" {
		subPath = strings.TrimPrefix(mount.Source, "/")
	}

	return corev1.VolumeMount{
		Name:      "devpod",
		MountPath: mount.Target,
		SubPath:   fmt.Sprintf("devpod/%s", subPath),
	}
}

func getLabels(pod *corev1.Pod, rawLabels string) (map[string]string, error) {
	labels := map[string]string{}
	maps.Copy(labels, pod.Labels)
	if rawLabels != "" {
		extraLabels, err := parseLabels(rawLabels)
		if err != nil {
			return nil, fmt.Errorf("parse labels: %w", err)
		}
		maps.Copy(labels, extraLabels)
	}
	// make sure we don't overwrite the devpod labels
	maps.Copy(labels, ExtraDevPodLabels)

	return labels, nil
}

func getNodeSelector(pod *corev1.Pod, rawNodeSelector string) (map[string]string, error) {
	nodeSelector := map[string]string{}
	maps.Copy(nodeSelector, pod.Spec.NodeSelector)

	if rawNodeSelector != "" {
		selector, err := parseLabels(rawNodeSelector)
		if err != nil {
			return nil, fmt.Errorf("parsing node selector: %w", err)
		}
		maps.Copy(nodeSelector, selector)
	}

	return nodeSelector, nil
}

func (k *KubernetesDriver) StartDevContainer(ctx context.Context, workspaceId string) error {
	workspaceId = getID(workspaceId)
	_, containerInfo, err := k.getDevContainerPvc(ctx, workspaceId)
	if err != nil {
		return err
	} else if containerInfo == nil {
		return fmt.Errorf("persistent volume '%s' not found", workspaceId)
	}

	return k.runContainer(
		ctx,
		workspaceId,
		containerInfo.Options,
		false,
	)
}

func getID(workspaceID string) string {
	return "devpod-" + workspaceID
}

func getPullSecretsName(workspaceID string) string {
	return fmt.Sprintf("devpod-pull-secret-%s", workspaceID)
}
