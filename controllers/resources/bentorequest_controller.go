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

	"github.com/pkg/errors"
	corev1 "k8s.io/api/core/v1"
	k8serrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/tools/record"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/log"
	"sigs.k8s.io/controller-runtime/pkg/predicate"

	resourcesv1alpha1 "github.com/bentoml/yatai-image-builder/apis/resources/v1alpha1"
	resourcesclient "github.com/bentoml/yatai-image-builder/generated/resources/clientset/versioned/typed/resources/v1alpha1"
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
			return
		}
		logs.Error(err, "Failed to reconcile BentoRequest.")
		r.Recorder.Eventf(bentoRequest, corev1.EventTypeWarning, "ReconcileError", "Failed to reconcile BentoRequest: %v", err)
	}()

	pod, bento, imageName, dockerConfigJSONSecretName, err := services.ImageBuilderService.CreateImageBuilderPod(ctx, services.CreateImageBuilderPodOption{
		BentoRequest:     bentoRequest,
		EventRecorder:    r.Recorder,
		Logger:           logs,
		RecreateIfFailed: true,
	})
	if err != nil {
		return
	}

	if pod != nil {
		err = ctrl.SetControllerReference(bentoRequest, pod, r.Scheme)
		if err != nil {
			return
		}
	}

	bentoCR := resourcesv1alpha1.Bento{
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

	restConf := ctrl.GetConfigOrDie()
	bentocli, err := resourcesclient.NewForConfig(restConf)
	if err != nil {
		err = errors.Wrap(err, "create Bento client")
		return
	}

	_, err = bentocli.Bentos(bentoRequest.Namespace).Create(ctx, &bentoCR, metav1.CreateOptions{})
	err = errors.Wrap(err, "create Bento resource")
	if k8serrors.IsAlreadyExists(err) {
		_, err = bentocli.Bentos(bentoRequest.Namespace).Update(ctx, &bentoCR, metav1.UpdateOptions{})
		err = errors.Wrap(err, "update Bento resource")
	}

	return
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
