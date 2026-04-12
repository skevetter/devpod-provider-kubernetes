package kubernetes

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/loft-sh/devpod/pkg/command"
	"github.com/skevetter/devpod-provider-kubernetes/pkg/throttledlogger"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/util/wait"
)

func (k *KubernetesDriver) waitPodRunning(ctx context.Context, id string) error {
	throttledLogger := throttledlogger.NewThrottledLogger(k.Log, time.Second*5)

	timeoutDuration, err := time.ParseDuration(k.options.PodTimeout)
	if err != nil {
		return fmt.Errorf("parse pod timeout: %w", err)
	}

	started := time.Now()
	var pod *corev1.Pod
	err = wait.PollUntilContextTimeout(
		ctx,
		time.Second,
		timeoutDuration,
		true,
		func(ctx context.Context) (bool, error) {
			var pollErr error
			pod, pollErr = k.getPod(ctx, id)
			if pollErr != nil {
				return false, pollErr
			} else if pod == nil {
				return true, nil
			}

			return k.checkPodProgress(ctx, pod, id, started, throttledLogger)
		},
	)

	return err
}

//nolint:revive // 5 params needed for pod progress check context.
func (k *KubernetesDriver) checkPodProgress(
	ctx context.Context,
	pod *corev1.Pod,
	id string,
	started time.Time,
	throttledLogger *throttledlogger.ThrottledLogger,
) (bool, error) {
	if pod.DeletionTimestamp != nil {
		throttledLogger.Infof("Waiting, since pod '%s' is terminating", id)
		return false, nil
	}

	condMsg := buildConditionMessage(started, pod)

	ready, err := checkInitContainerStatuses(pod, id, throttledLogger)
	if !ready || err != nil {
		return ready, err
	}

	if len(pod.Status.ContainerStatuses) < len(pod.Spec.Containers) {
		msg := fmt.Sprintf("Waiting, since pod '%s' is starting", id)
		if condMsg != "" {
			msg += fmt.Sprintf("\n%s", strings.TrimSpace(condMsg))
		}
		throttledLogger.Infof("%s", msg)
		return false, nil
	}

	return k.checkContainerStatuses(ctx, pod, id, throttledLogger)
}

func checkInitContainerStatuses(
	pod *corev1.Pod,
	id string,
	throttledLogger *throttledlogger.ThrottledLogger,
) (bool, error) {
	for _, c := range pod.Status.InitContainerStatuses {
		ready, err := checkSingleInitContainer(pod, id, &c, throttledLogger)
		if !ready || err != nil {
			return ready, err
		}
	}
	return true, nil
}

func checkSingleInitContainer(
	pod *corev1.Pod,
	id string,
	c *corev1.ContainerStatus,
	throttledLogger *throttledlogger.ThrottledLogger,
) (bool, error) {
	if IsWaiting(c) {
		if IsCritical(c) {
			return false, fmt.Errorf(
				"pod '%s' init container '%s' is waiting to start: %s (%s)",
				id, c.Name, c.State.Waiting.Message, c.State.Waiting.Reason,
			)
		}
		throttledLogger.Infof(
			"Waiting, since pod '%s' init container '%s' is waiting to start: %s (%s)",
			id, c.Name, c.State.Waiting.Message, c.State.Waiting.Reason,
		)
		return false, nil
	}

	if IsTerminated(c) && !Succeeded(c) {
		return false, fmt.Errorf(
			"pod '%s' init container '%s' is terminated: %s (%s)",
			id, c.Name, c.State.Terminated.Message, c.State.Terminated.Reason,
		)
	}

	container, err := getContainer(pod.Spec.InitContainers, c.Name)
	if err != nil {
		throttledLogger.Infof("Could not find container '%s'", c.Name)
		return false, err
	}

	return checkInitContainerReadiness(container, c, id, throttledLogger)
}

func checkInitContainerReadiness(
	container *corev1.Container,
	c *corev1.ContainerStatus,
	id string,
	throttledLogger *throttledlogger.ThrottledLogger,
) (bool, error) {
	if restartableInitContainer(container.RestartPolicy) {
		if !IsStarted(c) || !IsReady(c) {
			throttledLogger.Infof(
				"Waiting, since pod '%s' init container '%s' is not ready yet",
				id, c.Name,
			)
			return false, nil
		}
	} else {
		if IsRunning(c) {
			throttledLogger.Infof(
				"Waiting, since pod '%s' init container '%s' is running",
				id, c.Name,
			)
			return false, nil
		}
	}
	return true, nil
}

func (k *KubernetesDriver) checkContainerStatuses(
	ctx context.Context,
	pod *corev1.Pod,
	id string,
	throttledLogger *throttledlogger.ThrottledLogger,
) (bool, error) {
	for _, c := range pod.Status.ContainerStatuses {
		ready, err := k.checkSingleContainer(ctx, id, &c, throttledLogger)
		if !ready || err != nil {
			return ready, err
		}
	}
	return true, nil
}

func (k *KubernetesDriver) checkSingleContainer(
	ctx context.Context,
	id string,
	c *corev1.ContainerStatus,
	throttledLogger *throttledlogger.ThrottledLogger,
) (bool, error) {
	if IsTerminated(c) && Succeeded(c) {
		k.Log.Debugf("Delete Pod '%s' because it is succeeded", id)
		err := k.deletePod(ctx, id)
		if err != nil {
			return false, err
		}
		return false, nil
	}

	if IsWaiting(c) {
		if IsCritical(c) {
			return false, fmt.Errorf(
				"pod '%s' container '%s' is waiting to start: %s (%s)",
				id, c.Name, c.State.Waiting.Message, c.State.Waiting.Reason,
			)
		}
		throttledLogger.Infof(
			"Waiting, since pod '%s' container '%s' is waiting to start: %s (%s)",
			id, c.Name, c.State.Waiting.Message, c.State.Waiting.Reason,
		)
		return false, nil
	}

	if IsTerminated(c) {
		return false, fmt.Errorf(
			"pod '%s' container '%s' is terminated: %s (%s)",
			id, c.Name, c.State.Terminated.Message, c.State.Terminated.Reason,
		)
	}

	if !IsReady(c) {
		throttledLogger.Infof(
			"Waiting, since pod '%s' container '%s' is not ready yet",
			id, c.Name,
		)
		return false, nil
	}
	return true, nil
}

func (k *KubernetesDriver) getPod(ctx context.Context, id string) (*corev1.Pod, error) {
	// try to find pod
	out, err := k.buildCmd(ctx, []string{"get", "pod", id, "--ignore-not-found", "-o", "json"}).
		Output()
	if err != nil {
		return nil, fmt.Errorf("find container: %w", command.WrapCommandError(out, err))
	} else if len(out) == 0 {
		return nil, nil
	}

	// try to unmarshal pod
	pod := &corev1.Pod{}
	err = json.Unmarshal(out, pod)
	if err != nil {
		return nil, fmt.Errorf("unmarshal pod: %w", err)
	}

	return pod, nil
}

func getContainer(containers []corev1.Container, name string) (*corev1.Container, error) {
	for _, c := range containers {
		if c.Name == name {
			return &c, nil
		}
	}

	return nil, fmt.Errorf("cannot find pod container with name %s", name)
}

func restartableInitContainer(p *corev1.ContainerRestartPolicy) bool {
	return p != nil && *p == corev1.ContainerRestartPolicyAlways
}

func buildConditionMessage(
	started time.Time,
	pod *corev1.Pod,
) string {
	if time.Since(started) <= 45*time.Second {
		return ""
	}

	var condMsg strings.Builder
	for _, cond := range pod.Status.Conditions {
		if cond.Status != corev1.ConditionFalse {
			continue
		}
		fmt.Fprintf(
			&condMsg, "Condition %q is %s\n",
			cond.Type, cond.Status,
		)
		if cond.Reason != "" {
			fmt.Fprintf(
				&condMsg, "%s Reason: %s\n",
				cond.Type, cond.Reason,
			)
		}
		if cond.Message != "" {
			fmt.Fprintf(
				&condMsg, "%s Message: %s\n",
				cond.Type, cond.Message,
			)
		}
	}

	return condMsg.String()
}

func (k *KubernetesDriver) waitPodDeleted(ctx context.Context, id string) error {
	out, err := k.buildCmd(ctx, []string{"delete", "pod", id, "--ignore-not-found", "--wait"}).
		Output()
	if err != nil {
		return fmt.Errorf("delete pod: %w", command.WrapCommandError(out, err))
	}

	return nil
}
