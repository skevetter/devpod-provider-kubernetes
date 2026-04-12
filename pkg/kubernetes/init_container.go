package kubernetes

import (
	"fmt"
	"strings"

	"github.com/loft-sh/devpod/pkg/driver"
	corev1 "k8s.io/api/core/v1"
)

func (k *KubernetesDriver) getInitContainers(
	options *driver.RunOptions,
	pod *corev1.Pod,
	initialize bool,
) []corev1.Container {
	if !initialize {
		return filterOutInitContainer(pod)
	}

	commands, volumeMounts := buildInitMounts(options)

	retContainers, existing := splitExistingInit(pod)

	if len(volumeMounts) == 0 {
		return retContainers
	}

	initContainer := k.buildInitContainer(
		options.Image, commands, volumeMounts, existing,
	)

	return append(retContainers, initContainer)
}

func filterOutInitContainer(pod *corev1.Pod) []corev1.Container {
	retContainers := []corev1.Container{}
	for _, container := range pod.Spec.InitContainers {
		if container.Name == InitContainerName {
			continue
		}
		retContainers = append(retContainers, container)
	}
	return retContainers
}

func buildInitMounts(
	options *driver.RunOptions,
) ([]string, []corev1.VolumeMount) {
	commands := []string{}
	volumeMounts := []corev1.VolumeMount{}
	for idx, mount := range options.Mounts {
		if mount.Type != volumeType {
			continue
		}
		volumeMount := getVolumeMount(idx+1, mount)
		copyFrom := volumeMount.MountPath
		volumeMount.MountPath = "/" + volumeMount.SubPath
		volumeMounts = append(volumeMounts, volumeMount)
		commands = append(
			commands,
			fmt.Sprintf(
				`cp -a %s/. %s/ || true`,
				strings.TrimRight(copyFrom, "/"),
				strings.TrimRight(volumeMount.MountPath, "/"),
			),
		)
	}
	return commands, volumeMounts
}

func splitExistingInit(
	pod *corev1.Pod,
) ([]corev1.Container, *corev1.Container) {
	retContainers := []corev1.Container{}
	var existing *corev1.Container
	for i, container := range pod.Spec.InitContainers {
		if container.Name == InitContainerName {
			existing = &pod.Spec.InitContainers[i]
		} else {
			retContainers = append(retContainers, container)
		}
	}
	return retContainers, existing
}

func (k *KubernetesDriver) buildInitContainer(
	image string,
	commands []string,
	volumeMounts []corev1.VolumeMount,
	existing *corev1.Container,
) corev1.Container {
	securityContext := &corev1.SecurityContext{
		RunAsUser:    &[]int64{0}[0],
		RunAsGroup:   &[]int64{0}[0],
		RunAsNonRoot: &[]bool{false}[0],
	}
	if k.options.StrictSecurity {
		securityContext = nil
	}

	resources := corev1.ResourceRequirements{}
	if existing != nil {
		resources = existing.Resources
	}
	if k.options.HelperResources != "" {
		resources = parseResources(k.options.HelperResources, k.Log)
	}

	initContainer := corev1.Container{
		Name:            InitContainerName,
		Image:           image,
		Command:         []string{"sh"},
		Args:            []string{"-c", strings.Join(commands, "\n") + "\n"},
		Resources:       resources,
		VolumeMounts:    volumeMounts,
		SecurityContext: securityContext,
	}

	if existing != nil {
		mergeExistingInit(&initContainer, existing)
	}

	return initContainer
}

func mergeExistingInit(
	initContainer *corev1.Container,
	existing *corev1.Container,
) {
	initContainer.Env = append(
		existing.Env, initContainer.Env...,
	)
	initContainer.EnvFrom = existing.EnvFrom
	initContainer.Ports = existing.Ports
	initContainer.VolumeMounts = append(
		existing.VolumeMounts,
		initContainer.VolumeMounts...)
	initContainer.ImagePullPolicy = existing.ImagePullPolicy

	if initContainer.SecurityContext == nil &&
		existing.SecurityContext != nil {
		initContainer.SecurityContext = existing.SecurityContext
	}
}
