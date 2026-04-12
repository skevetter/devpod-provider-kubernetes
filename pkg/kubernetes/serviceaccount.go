package kubernetes

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"

	"github.com/loft-sh/devpod/pkg/command"
	corev1 "k8s.io/api/core/v1"
	rbacv1 "k8s.io/api/rbac/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

func (k *KubernetesDriver) createServiceAccount(
	ctx context.Context,
	id, serviceAccount string,
) error {
	err := k.ensureServiceAccount(ctx, serviceAccount)
	if err != nil {
		return err
	}

	if k.options.ClusterRole != "" {
		err = k.ensureRoleBinding(ctx, id, serviceAccount)
		if err != nil {
			return err
		}
	}

	return nil
}

func (k *KubernetesDriver) ensureServiceAccount(
	ctx context.Context,
	serviceAccount string,
) error {
	out, err := k.buildCmd(
		ctx,
		[]string{
			"get", "serviceaccount", serviceAccount,
			"--ignore-not-found", "-o", "json",
		},
	).Output()
	if err != nil {
		return command.WrapCommandError(out, err)
	}
	if len(out) != 0 {
		return nil
	}

	raw, err := json.Marshal(&corev1.ServiceAccount{
		TypeMeta: metav1.TypeMeta{
			Kind:       "ServiceAccount",
			APIVersion: corev1.SchemeGroupVersion.String(),
		},
		ObjectMeta: metav1.ObjectMeta{
			Name:   serviceAccount,
			Labels: ExtraDevPodLabels,
		},
	})
	if err != nil {
		return err
	}

	k.Log.Infof("Create Service Account '%s'", serviceAccount)
	buf := &bytes.Buffer{}
	err = k.runCommand(
		ctx,
		[]string{"create", "-f", "-"},
		cmdIO{stdin: bytes.NewReader(raw), stdout: buf, stderr: buf},
	)
	if err != nil {
		return fmt.Errorf(
			"create service account: %s: %w", buf.String(), err,
		)
	}

	return nil
}

func (k *KubernetesDriver) ensureRoleBinding(
	ctx context.Context,
	id, serviceAccount string,
) error {
	out, err := k.buildCmd(
		ctx,
		[]string{
			"get", "rolebinding", id,
			"--ignore-not-found", "-o", "json",
		},
	).Output()
	if err != nil {
		return command.WrapCommandError(out, err)
	}
	if len(out) != 0 {
		return nil
	}

	raw, err := json.Marshal(&rbacv1.RoleBinding{
		TypeMeta: metav1.TypeMeta{
			Kind:       "RoleBinding",
			APIVersion: rbacv1.SchemeGroupVersion.String(),
		},
		ObjectMeta: metav1.ObjectMeta{
			Name:   id,
			Labels: ExtraDevPodLabels,
		},
		Subjects: []rbacv1.Subject{
			{
				Kind: "ServiceAccount",
				Name: serviceAccount,
			},
		},
		RoleRef: rbacv1.RoleRef{
			APIGroup: rbacv1.SchemeGroupVersion.Group,
			Kind:     "ClusterRole",
			Name:     k.options.ClusterRole,
		},
	})
	if err != nil {
		return err
	}

	k.Log.Infof("Create Role Binding '%s'", serviceAccount)
	buf := &bytes.Buffer{}
	err = k.runCommand(
		ctx,
		[]string{"create", "-f", "-"},
		cmdIO{stdin: bytes.NewReader(raw), stdout: buf, stderr: buf},
	)
	if err != nil {
		return fmt.Errorf(
			"create role binding: %s: %w", buf.String(), err,
		)
	}

	return nil
}
