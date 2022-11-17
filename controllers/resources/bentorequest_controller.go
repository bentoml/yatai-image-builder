/*
Copyright 2022.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

package resources

import (
	"context"
	"reflect"
	"time"

	"github.com/pkg/errors"
	corev1 "k8s.io/api/core/v1"
	k8serrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/wait"
	"k8s.io/client-go/tools/record"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/log"
	"sigs.k8s.io/controller-runtime/pkg/predicate"

	"github.com/bentoml/yatai-schemas/modelschemas"

	resourcesv1alpha1 "github.com/bentoml/yatai-image-builder/apis/resources/v1alpha1"
	"github.com/bentoml/yatai-image-builder/services"
)

// BentoRequestReconciler reconciles a BentoRequest object
type BentoRequestReconciler struct {
	client.Client
	Scheme   *runtime.Scheme
	Recorder record.EventRecorder
}

//+kubebuilder:rbac:groups=resources.yatai.ai,resources=bentorequests,verbs=get;list;watch;create;update;patch;delete
//+kubebuilder:rbac:groups=resources.yatai.ai,resources=bentorequests/status,verbs=get;update;patch
//+kubebuilder:rbac:groups=resources.yatai.ai,resources=bentorequests/finalizers,verbs=update
//+kubebuilder:rbac:groups=resources.yatai.ai,resources=bentoes,verbs=get;list;watch;create;update;patch;delete
//+kubebuilder:rbac:groups=events.k8s.io,resources=events,verbs=get;list;watch;create;update;patch;delete
//+kubebuilder:rbac:groups=core,resources=events,verbs=get;list;watch;create;update;patch;delete
//+kubebuilder:rbac:groups=core,resources=pods,verbs=get;list;watch;create;update;patch;delete

// Reconcile is part of the main kubernetes reconciliation loop which aims to
// move the current state of the cluster closer to the desired state.
// TODO(user): Modify the Reconcile function to compare the state specified by
// the BentoRequest object against the actual cluster state, and then
// perform operations to make the cluster state reflect the state specified by
// the user.
//
// For more details, check Reconcile and its Result here:
// - https://pkg.go.dev/sigs.k8s.io/controller-runtime@v0.12.2/pkg/reconcile
func (r *BentoRequestReconciler) Reconcile(ctx context.Context, req ctrl.Request) (result ctrl.Result, err error) {
	logs := log.FromContext(ctx)

	bentoRequest := &resourcesv1alpha1.BentoRequest{}

	err = r.Get(ctx, req.NamespacedName, bentoRequest)

	if err != nil {
		if k8serrors.IsNotFound(err) {
			// Object not found, return.  Created objects are automatically garbage collected.
			// For additional cleanup logic use finalizers.
			logs.Info("BentoRequest resource not found. Ignoring since object must be deleted.")
			err = nil
			return
		}
		// Error reading the object - requeue the request.
		logs.Error(err, "Failed to get BentoRequest.")
		return
	}

	logs = logs.WithValues("bentoRequest", bentoRequest.Name, "bentoRequestNamespace", bentoRequest.Namespace)

	defer func() {
		if err == nil {
			logs.Info("Reconcile success")
			return
		}
		logs.Error(err, "Failed to reconcile BentoRequest.")
		r.Recorder.Eventf(bentoRequest, corev1.EventTypeWarning, "ReconcileError", "Failed to reconcile BentoRequest: %v", err)
	}()

	err = r.Get(ctx, req.NamespacedName, bentoRequest)
	if err != nil {
		return
	}
	bentoRequest.Status.BentoGenerated = false
	bentoRequest.Status.Reconciled = false
	err = r.Status().Update(ctx, bentoRequest)
	if err != nil {
		err = errors.Wrap(err, "update BentoRequest status")
		return
	}

	pod, bento, imageName, dockerConfigJSONSecretName, err := services.ImageBuilderService.CreateImageBuilderPod(ctx, services.CreateImageBuilderPodOption{
		BentoRequest:     bentoRequest,
		EventRecorder:    r.Recorder,
		Logger:           logs,
		RecreateIfFailed: true,
		Client:           r.Client,
		Scheme:           r.Scheme,
	})
	if err != nil {
		return
	}

	imageBuildStatus := modelschemas.ImageBuildStatusPending
	errorMessage := ""

	err = r.Get(ctx, req.NamespacedName, bentoRequest)
	if err != nil {
		return
	}
	bentoRequest.Status.ImageBuildStatus = imageBuildStatus
	bentoRequest.Status.ErrorMessage = errorMessage
	err = r.Status().Update(ctx, bentoRequest)
	if err != nil {
		err = errors.Wrap(err, "update BentoRequest status")
		return
	}

	if pod != nil {
		r.Recorder.Eventf(bentoRequest, corev1.EventTypeNormal, "BentoImageBuilder", "Building image %s..., the image builder pod is %s in namespace %s", imageName, pod.Name, pod.Namespace)

		imageBuildStatus, err = r.waitImageBuilderPodComplete(ctx, bentoRequest, pod.Namespace, pod.Name)

		if err != nil {
			r.Recorder.Eventf(bentoRequest, corev1.EventTypeWarning, "BentoImageBuilder", "Failed to build image %s, the image builder pod is %s in namespace %s has an error: %s", imageName, pod.Name, pod.Namespace, err.Error())
			err = errors.Wrapf(err, "failed to build image %s for bento %s", imageName, bentoRequest.Spec.BentoTag)
			errorMessage = err.Error()
			err = nil
		}

		r.Recorder.Eventf(bentoRequest, corev1.EventTypeNormal, "BentoImageBuilder", "Image %s has been built successfully", imageName)
	}

	bentoGenerated := false
	if pod == nil || imageBuildStatus == modelschemas.ImageBuildStatusSuccess {
		imageBuildStatus = modelschemas.ImageBuildStatusSuccess
		bentoGenerated = true
		bentoCR := &resourcesv1alpha1.Bento{
			ObjectMeta: metav1.ObjectMeta{
				Name:      bentoRequest.Name,
				Namespace: bentoRequest.Namespace,
			},
			Spec: resourcesv1alpha1.BentoSpec{
				Image:   imageName,
				Context: bentoRequest.Spec.Context,
				Runners: bentoRequest.Spec.Runners,
			},
		}

		err = ctrl.SetControllerReference(bentoRequest, bentoCR, r.Scheme)
		if err != nil {
			err = errors.Wrap(err, "set controller reference")
			return
		}

		if dockerConfigJSONSecretName != "" {
			bentoCR.Spec.ImagePullSecrets = []corev1.LocalObjectReference{
				{
					Name: dockerConfigJSONSecretName,
				},
			}
		}

		if bento != nil {
			bentoCR.Spec.Context = resourcesv1alpha1.BentoContext{
				BentomlVersion: bento.Manifest.BentomlVersion,
			}
			bentoCR.Spec.Runners = make([]resourcesv1alpha1.BentoRunner, 0)
			for _, runner := range bento.Manifest.Runners {
				bentoCR.Spec.Runners = append(bentoCR.Spec.Runners, resourcesv1alpha1.BentoRunner{
					Name:         runner.Name,
					RunnableType: runner.RunnableType,
					ModelTags:    runner.Models,
				})
			}
		}

		r.Recorder.Eventf(bentoRequest, corev1.EventTypeNormal, "BentoImageBuilder", "Creating Bento CR %s in namespace %s", bentoCR.Name, bentoCR.Namespace)
		err = r.Create(ctx, bentoCR)
		isAlreadyExists := k8serrors.IsAlreadyExists(err)
		if err != nil && !isAlreadyExists {
			err = errors.Wrap(err, "create Bento resource")
			return
		}
		if isAlreadyExists {
			oldBentoCR := &resourcesv1alpha1.Bento{}
			r.Recorder.Eventf(bentoRequest, corev1.EventTypeNormal, "BentoImageBuilder", "Updating Bento CR %s in namespace %s", bentoCR.Name, bentoCR.Namespace)
			err = r.Get(ctx, types.NamespacedName{Name: bentoCR.Name, Namespace: bentoCR.Namespace}, oldBentoCR)
			if err != nil {
				err = errors.Wrap(err, "get Bento resource")
				return
			}
			if !reflect.DeepEqual(oldBentoCR.Spec, bentoCR.Spec) {
				oldBentoCR.OwnerReferences = bentoCR.OwnerReferences
				oldBentoCR.Spec = bentoCR.Spec
				err = r.Update(ctx, oldBentoCR)
				if err != nil {
					err = errors.Wrap(err, "update Bento resource")
					return
				}
			}
		}
	}

	status := resourcesv1alpha1.BentoRequestStatus{
		BentoGenerated:   bentoGenerated,
		Reconciled:       true,
		ErrorMessage:     errorMessage,
		ImageBuildStatus: imageBuildStatus,
	}

	err = r.Get(ctx, req.NamespacedName, bentoRequest)
	if err != nil {
		return
	}
	if !reflect.DeepEqual(status, bentoRequest.Status) {
		bentoRequest.Status = status
		err = r.Status().Update(ctx, bentoRequest)
		if err != nil {
			err = errors.Wrap(err, "update BentoRequest status")
			return
		}
	}

	return
}

func (r *BentoRequestReconciler) waitImageBuilderPodComplete(ctx context.Context, bentoRequest *resourcesv1alpha1.BentoRequest, namespace, podName string) (modelschemas.ImageBuildStatus, error) {
	logs := log.Log.WithValues("func", "waitImageBuilderPodComplete", "namespace", namespace, "pod", podName)

	// Interval to poll for objects.
	pollInterval := 3 * time.Second

	// How long to wait for objects.
	waitTimeout := 60 * time.Minute

	if bentoRequest.Spec.ImageBuildTimeout != nil {
		waitTimeout = *bentoRequest.Spec.ImageBuildTimeout
	}

	imageBuildStatus := modelschemas.ImageBuildStatusPending

	// Wait for the image builder pod to be Complete.
	if err := wait.PollImmediate(pollInterval, waitTimeout, func() (done bool, err error) {
		pod := &corev1.Pod{}
		err_ := r.Get(ctx, types.NamespacedName{Namespace: namespace, Name: podName}, pod)
		if err_ != nil {
			logs.Error(err_, "failed to get pod")
			err_ = errors.Wrap(err_, "failed to get pod")
			return true, err_
		}
		if pod.Status.Phase == corev1.PodSucceeded {
			imageBuildStatus = modelschemas.ImageBuildStatusSuccess
			return true, nil
		}
		if pod.Status.Phase == corev1.PodFailed {
			imageBuildStatus = modelschemas.ImageBuildStatusFailed
			return true, errors.Errorf("pod %s in namespace %s failed", pod.Name, pod.Namespace)
		}
		if pod.Status.Phase == corev1.PodUnknown {
			imageBuildStatus = modelschemas.ImageBuildStatusFailed
			return true, errors.Errorf("pod %s in namespace %s is in unknown state", pod.Name, pod.Namespace)
		}
		if pod.Status.Phase == corev1.PodRunning {
			imageBuildStatus = modelschemas.ImageBuildStatusBuilding
		}
		return false, nil
	}); err != nil {
		err = errors.Wrapf(err, "failed to wait for pod %s in namespace %s to be ready", podName, namespace)
		return imageBuildStatus, err
	}
	return imageBuildStatus, nil
}

// SetupWithManager sets up the controller with the Manager.
func (r *BentoRequestReconciler) SetupWithManager(mgr ctrl.Manager) error {
	pred := predicate.GenerationChangedPredicate{}
	err := ctrl.NewControllerManagedBy(mgr).
		For(&resourcesv1alpha1.BentoRequest{}).
		Owns(&resourcesv1alpha1.Bento{}).
		Owns(&corev1.Pod{}).
		WithEventFilter(pred).
		Complete(r)
	return errors.Wrap(err, "failed to setup BentoRequest controller")
}
