package kubernetes

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"strings"

	"github.com/loft-sh/devpod/pkg/encoding"
	"github.com/loft-sh/devpod/pkg/random"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

func (k *KubernetesDriver) TargetArchitecture(
	ctx context.Context,
	workspaceId string,
) (string, error) {
	workspaceId = getID(workspaceId)

	k.ensureNamespace(ctx)

	pod, err := k.buildArchDetectionPod(workspaceId)
	if err != nil {
		return "", err
	}

	return k.detectArchitecture(ctx, pod)
}

func (k *KubernetesDriver) ensureNamespace(ctx context.Context) {
	if k.namespace != "" && k.options.CreateNamespace == "true" {
		k.Log.Debugf("Create namespace '%s'", k.namespace)
		buf := &bytes.Buffer{}
		err := k.runCommand(
			ctx,
			[]string{"create", "ns", k.namespace},
			cmdIO{stdout: buf, stderr: buf},
		)
		if err != nil {
			k.Log.Debugf("Error creating namespace: %v", err)
		}
	}
}

func (k *KubernetesDriver) buildArchDetectionPod(
	workspaceId string,
) (*corev1.Pod, error) {
	pod := &corev1.Pod{
		TypeMeta: metav1.TypeMeta{
			Kind:       "Pod",
			APIVersion: corev1.SchemeGroupVersion.String(),
		},
	}
	if len(k.options.ArchDetectionPodManifestTemplate) > 0 {
		k.Log.Debugf(
			"trying to get arch detection pod template manifest from %s",
			k.options.ArchDetectionPodManifestTemplate,
		)
		p, err := getPodTemplate(
			k.options.ArchDetectionPodManifestTemplate,
		)
		if err != nil {
			return nil, err
		}
		pod = p
	}

	podName := encoding.SafeConcatNameMax(
		[]string{"devpod", workspaceId, random.String(6)}, 32,
	)
	pod.Namespace = k.namespace
	pod.Name = podName

	labels := map[string]string{}
	if pod.Labels == nil {
		pod.Labels = map[string]string{}
	}
	for k, label := range pod.Labels {
		labels[k] = label
	}
	labels[DevPodWorkspaceLabel] = workspaceId
	pod.Labels = labels

	pod.Spec.RestartPolicy = corev1.RestartPolicyNever
	pod.Spec.Containers = getArchitectureDetectionPodContainers(
		pod,
		k.helperImage(),
		[]string{"sh", "-c", "uname -m && tail -f /dev/null"},
	)

	return pod, nil
}

func (k *KubernetesDriver) detectArchitecture(
	ctx context.Context,
	pod *corev1.Pod,
) (string, error) {
	podRaw, err := json.Marshal(pod)
	if err != nil {
		return "", err
	}

	k.Log.Infof("Find out cluster architecture...")
	stdout := &bytes.Buffer{}
	stderr := &bytes.Buffer{}
	err = k.runCommand(
		ctx,
		[]string{"create", "-f", "-"},
		cmdIO{stdin: strings.NewReader(string(podRaw)), stdout: stdout, stderr: stderr},
	)
	if err != nil {
		return "", fmt.Errorf(
			"find out cluster architecture: %s %s %w",
			stdout.String(), stderr.String(), err,
		)
	}

	k.Log.Infof("Waiting for cluster architecture job to come up...")
	_, err = k.waitPodRunning(ctx, pod.Name)
	if err != nil {
		return "", fmt.Errorf(
			"find out cluster architecture: %s %s %w",
			stdout.String(), stderr.String(), err,
		)
	}

	err = k.runCommand(
		ctx,
		[]string{"logs", pod.Name, "-n", k.namespace},
		cmdIO{stdin: os.Stdin, stdout: stdout, stderr: stderr},
	)
	if err != nil {
		return "", fmt.Errorf(
			"find out cluster architecture: %s %s %w",
			stdout.String(), stderr.String(), err,
		)
	}

	unameOutput := stdout.String()
	if strings.Contains(unameOutput, "arm") ||
		strings.Contains(unameOutput, "aarch") {
		return "arm64", nil
	}

	return "amd64", nil
}

func (k *KubernetesDriver) helperImage() string {
	if k.options.HelperImage != "" {
		return k.options.HelperImage
	}

	return "busybox:latest"
}

func getArchitectureDetectionPodContainers(
	pod *corev1.Pod,
	imageName string,
	args []string,
) []corev1.Container {
	devPodContainer := corev1.Container{
		Name:  DevContainerName,
		Image: imageName,
		Args:  args,
	}

	// merge with existing container if it exists
	var existingDevPodContainer *corev1.Container
	retContainers := []corev1.Container{}
	if pod != nil {
		for i, container := range pod.Spec.Containers {
			if container.Name == DevContainerName {
				existingDevPodContainer = &pod.Spec.Containers[i]
			} else {
				retContainers = append(retContainers, container)
			}
		}
	}

	if existingDevPodContainer != nil {
		devPodContainer.Env = append(
			existingDevPodContainer.Env, devPodContainer.Env...,
		)
		devPodContainer.EnvFrom = existingDevPodContainer.EnvFrom
		devPodContainer.Ports = existingDevPodContainer.Ports
		devPodContainer.VolumeMounts = append(
			existingDevPodContainer.VolumeMounts,
			devPodContainer.VolumeMounts...)
		devPodContainer.ImagePullPolicy = existingDevPodContainer.ImagePullPolicy
		devPodContainer.Resources = existingDevPodContainer.Resources

		if devPodContainer.SecurityContext == nil &&
			existingDevPodContainer.SecurityContext != nil {
			devPodContainer.SecurityContext = existingDevPodContainer.SecurityContext
		}
	}
	retContainers = append(retContainers, devPodContainer)

	return retContainers
}
