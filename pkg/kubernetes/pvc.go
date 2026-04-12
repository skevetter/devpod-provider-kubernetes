package kubernetes

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"maps"
	"strings"

	"github.com/skevetter/devpod/pkg/driver"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

func (k *KubernetesDriver) createPersistentVolumeClaim(
	ctx context.Context,
	id string,
	options *driver.RunOptions,
) error {
	pvcString, err := k.buildPersistentVolumeClaim(id, options)
	if err != nil {
		return err
	}

	k.Log.Infof("Create Persistent Volume Claim '%s'", id)
	buf := &bytes.Buffer{}
	err = k.runCommand(
		ctx,
		[]string{"create", "-f", "-"},
		cmdIO{stdin: strings.NewReader(pvcString), stdout: buf, stderr: buf},
	)
	if err != nil {
		return fmt.Errorf("create pvc: %s: %w", buf.String(), err)
	}

	return nil
}

func (k *KubernetesDriver) buildPersistentVolumeClaim(
	id string,
	options *driver.RunOptions,
) (string, error) {
	containerInfo, err := k.getDevContainerInformation(id, options)
	if err != nil {
		return "", err
	}
	size := "10Gi"
	if k.options.DiskSize != "" {
		size = k.options.DiskSize
	}
	quantity, err := resource.ParseQuantity(size)
	if err != nil {
		return "", fmt.Errorf("parse persistent volume size '%s': %w", size, err)
	}
	var storageClassName *string
	if k.options.StorageClass != "" {
		storageClassName = &k.options.StorageClass
	}
	accessMode, err := parseAccessMode(k.options.PvcAccessMode)
	if err != nil {
		return "", err
	}
	labels := map[string]string{DevPodWorkspaceUIDLabel: options.UID}
	maps.Copy(labels, ExtraDevPodLabels)

	annotations := map[string]string{DevPodInfoAnnotation: containerInfo}
	extraAnnotations, err := parseLabels(k.options.PvcAnnotations)
	if err != nil {
		k.Log.Error("Failed to parse annotations from PVC_ANNOTATIONS option: %v", err)
	}
	maps.Copy(annotations, extraAnnotations)

	pvc := &corev1.PersistentVolumeClaim{
		TypeMeta: metav1.TypeMeta{
			Kind:       "PersistentVolumeClaim",
			APIVersion: corev1.SchemeGroupVersion.String(),
		},
		ObjectMeta: metav1.ObjectMeta{
			Name:        id,
			Labels:      labels,
			Annotations: annotations,
		},
		Spec: corev1.PersistentVolumeClaimSpec{
			AccessModes: accessMode,
			Resources: corev1.VolumeResourceRequirements{
				Requests: map[corev1.ResourceName]resource.Quantity{
					corev1.ResourceStorage: quantity,
				},
			},
			StorageClassName: storageClassName,
		},
	}

	raw, err := json.Marshal(pvc)
	if err != nil {
		return "", err
	}

	return string(raw), nil
}

func (k *KubernetesDriver) getDevContainerInformation(
	id string,
	options *driver.RunOptions,
) (string, error) {
	containerInfo, err := json.Marshal(&DevContainerInfo{
		WorkspaceID: id,
		Options:     options,
	})
	if err != nil {
		return "", err
	}

	return string(containerInfo), nil
}

func parseAccessMode(mode string) ([]corev1.PersistentVolumeAccessMode, error) {
	switch mode {
	case "", "RWO":
		return []corev1.PersistentVolumeAccessMode{corev1.ReadWriteOnce}, nil
	case "ROX":
		return []corev1.PersistentVolumeAccessMode{corev1.ReadOnlyMany}, nil
	case "RWX":
		return []corev1.PersistentVolumeAccessMode{corev1.ReadWriteMany}, nil
	case "RWOP":
		return []corev1.PersistentVolumeAccessMode{corev1.ReadWriteOncePod}, nil
	default:
		return nil, fmt.Errorf(
			"unsupported PVC access mode %q, valid values: RWO, ROX, RWX, RWOP",
			mode,
		)
	}
}
