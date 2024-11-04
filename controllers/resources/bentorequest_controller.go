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
	"path"

	// nolint: gosec
	"crypto/md5"
	"strconv"

	"fmt"
	"os"
	"reflect"
	"time"

	"github.com/apparentlymart/go-shquot/shquot"
	"github.com/mitchellh/hashstructure/v2"
	"github.com/pkg/errors"
	"github.com/rs/xid"
	"github.com/sergeymakinen/go-quote/unix"
	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	k8serrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/tools/record"
	"k8s.io/utils/pointer"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/builder"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
	"sigs.k8s.io/controller-runtime/pkg/log"
	"sigs.k8s.io/controller-runtime/pkg/predicate"
	"sigs.k8s.io/yaml"

	"bytes"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"path/filepath"
	"strings"
	"text/template"

	"github.com/huandu/xstrings"
	"github.com/iancoleman/strcase"
	"github.com/prune998/docker-registry-client/registry"
	"github.com/sirupsen/logrus"
	"k8s.io/apimachinery/pkg/api/resource"

	commonconfig "github.com/bentoml/yatai-common/config"
	commonconsts "github.com/bentoml/yatai-common/consts"
	"github.com/bentoml/yatai-schemas/modelschemas"
	"github.com/bentoml/yatai-schemas/schemasv1"

	resourcesv1alpha1 "github.com/bentoml/yatai-image-builder/apis/resources/v1alpha1"
	"github.com/bentoml/yatai-image-builder/version"
	yataiclient "github.com/bentoml/yatai-image-builder/yatai-client"

	"github.com/aws/aws-sdk-go/aws"
	"github.com/aws/aws-sdk-go/aws/awserr"
	"github.com/aws/aws-sdk-go/aws/session"
	"github.com/aws/aws-sdk-go/service/ecr"
)

const (
	// nolint: gosec
	YataiImageBuilderAWSAccessKeySecretName = "yatai-image-builder-aws-access-key"
	// nolint: gosec
	YataiImageBuilderGCPAccessKeySecretName   = "yatai-image-builder-gcp-access-key"
	KubeAnnotationBentoRequestHash            = "yatai.ai/bento-request-hash"
	KubeAnnotationBentoRequestImageBuiderHash = "yatai.ai/bento-request-image-builder-hash"
	KubeAnnotationBentoRequestModelSeederHash = "yatai.ai/bento-request-model-seeder-hash"
	KubeLabelYataiImageBuilderSeparateModels  = "yatai.ai/yatai-image-builder-separate-models"
	KubeAnnotationBentoStorageNS              = "yatai.ai/bento-storage-namepsace"
	KubeAnnotationModelStorageNS              = "yatai.ai/model-storage-namepsace"
	StoreSchemaAWS                            = "aws"
	StoreSchemaGCP                            = "gcp"
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
//+kubebuilder:rbac:groups=resources.yatai.ai,resources=bentoes/status,verbs=get;update;patch
//+kubebuilder:rbac:groups=events.k8s.io,resources=events,verbs=get;list;watch;create;update;patch;delete
//+kubebuilder:rbac:groups=core,resources=events,verbs=get;list;watch;create;update;patch;delete
//+kubebuilder:rbac:groups=core,resources=pods,verbs=get;list;watch;create;update;patch;delete
//+kubebuilder:rbac:groups=core,resources=configmaps,verbs=get;list;watch;create;update;patch;delete
//+kubebuilder:rbac:groups=core,resources=secrets,verbs=get;list;watch;create;update;patch;delete
//+kubebuilder:rbac:groups=core,resources=serviceaccounts,verbs=get;list;watch
//+kubebuilder:rbac:groups=coordination.k8s.io,resources=leases,verbs=get;list;watch;create;update;patch;delete

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
			logs.Info("BentoRequest resource not found. Ignoring since object must be deleted")
			err = nil
			return
		}
		// Error reading the object - requeue the request.
		logs.Error(err, "Failed to get BentoRequest")
		return
	}

	for _, condition := range bentoRequest.Status.Conditions {
		if condition.Type == resourcesv1alpha1.BentoRequestConditionTypeBentoAvailable && condition.Status == metav1.ConditionTrue {
			logs.Info("Skip available bentorequest")
			return
		}
	}

	if bentoRequest.Status.Conditions == nil || len(bentoRequest.Status.Conditions) == 0 {
		bentoRequest, err = r.setStatusConditions(ctx, req,
			metav1.Condition{
				Type:    resourcesv1alpha1.BentoRequestConditionTypeModelsSeeding,
				Status:  metav1.ConditionUnknown,
				Reason:  "Reconciling",
				Message: "Starting to reconcile BentoRequest",
			},
			metav1.Condition{
				Type:    resourcesv1alpha1.BentoRequestConditionTypeImageBuilding,
				Status:  metav1.ConditionUnknown,
				Reason:  "Reconciling",
				Message: "Starting to reconcile BentoRequest",
			},
			metav1.Condition{
				Type:    resourcesv1alpha1.BentoRequestConditionTypeImageExists,
				Status:  metav1.ConditionUnknown,
				Reason:  "Reconciling",
				Message: "Starting to reconcile BentoRequest",
			},
		)
		if err != nil {
			return
		}
	}

	logs = logs.WithValues("bentoRequest", bentoRequest.Name, "bentoRequestNamespace", bentoRequest.Namespace)

	defer func() {
		if err == nil {
			logs.Info("Reconcile success")
			return
		}
		logs.Error(err, "Failed to reconcile BentoRequest.")
		r.Recorder.Eventf(bentoRequest, corev1.EventTypeWarning, "ReconcileError", "Failed to reconcile BentoRequest: %v", err)
		_, err_ := r.setStatusConditions(ctx, req,
			metav1.Condition{
				Type:    resourcesv1alpha1.BentoRequestConditionTypeBentoAvailable,
				Status:  metav1.ConditionFalse,
				Reason:  "Reconciling",
				Message: err.Error(),
			},
		)
		if err_ != nil {
			logs.Error(err_, "Failed to update BentoRequest status")
			return
		}
	}()

	bentoAvailableCondition := meta.FindStatusCondition(bentoRequest.Status.Conditions, resourcesv1alpha1.BentoRequestConditionTypeBentoAvailable)
	if bentoAvailableCondition == nil || bentoAvailableCondition.Status != metav1.ConditionUnknown {
		bentoRequest, err = r.setStatusConditions(ctx, req,
			metav1.Condition{
				Type:    resourcesv1alpha1.BentoRequestConditionTypeBentoAvailable,
				Status:  metav1.ConditionUnknown,
				Reason:  "Reconciling",
				Message: "Reconciling",
			},
		)
		if err != nil {
			return
		}
	}

	separateModels := isSeparateModels(bentoRequest)

	modelsExists := false
	var modelsExistsResult ctrl.Result
	var modelsExistsErr error

	if separateModels {
		bentoRequest, modelsExists, modelsExistsResult, modelsExistsErr = r.ensureModelsExists(ctx, ensureModelsExistsOption{
			bentoRequest: bentoRequest,
			req:          req,
		})
	}

	bentoRequest, imageInfo, imageExists, imageExistsResult, err := r.ensureImageExists(ctx, ensureImageExistsOption{
		bentoRequest: bentoRequest,
		req:          req,
	})

	if err != nil {
		err = errors.Wrapf(err, "ensure image exists")
		return
	}

	if !imageExists {
		result = imageExistsResult
		bentoRequest, err = r.setStatusConditions(ctx, req,
			metav1.Condition{
				Type:    resourcesv1alpha1.BentoRequestConditionTypeBentoAvailable,
				Status:  metav1.ConditionUnknown,
				Reason:  "Reconciling",
				Message: "Bento image is building",
			},
		)
		if err != nil {
			return
		}
		return
	} else {
		if err = r.deleteImageBuilderJobs(ctx, bentoRequest); err != nil {
			r.Recorder.Eventf(bentoRequest, corev1.EventTypeWarning, "DeleteImageBuilderJobs", "Failed to delete image builder jobs: %v", err)
			log.FromContext(ctx).Error(err, "Failed to delete image builder jobs")
			// We don't return here to allow the reconciliation to continue
		}
	}

	if modelsExistsErr != nil {
		err = errors.Wrap(modelsExistsErr, "ensure model exists")
		return
	}

	if separateModels && modelsExists {
		// Delete PVCs for all seeded models
		for _, model := range bentoRequest.Spec.Models {
			model := model
			// Delete model seeder jobs
			if err = r.deleteModelSeederJobs(ctx, bentoRequest, &model); err != nil {
				r.Recorder.Eventf(bentoRequest, corev1.EventTypeWarning, "DeleteModelSeederJobs", "Failed to delete model seeder jobs: %v", err)
				log.FromContext(ctx).Error(err, "Failed to delete model seeder jobs")
				// We don't return here to allow the reconciliation to continue
			}

			err = r.deleteModelPVC(ctx, bentoRequest, &model)
			if err != nil {
				r.Recorder.Eventf(bentoRequest, corev1.EventTypeWarning, "DeletePVC", "Failed to delete PVC for model %s: %v", model.Tag, err)
				// Log the error but continue with other models
				log.FromContext(ctx).Error(err, "Failed to delete PVC", "model", model.Tag)
			}
		}
	}

	if separateModels && !modelsExists {
		result = modelsExistsResult
		bentoRequest, err = r.setStatusConditions(ctx, req,
			metav1.Condition{
				Type:    resourcesv1alpha1.BentoRequestConditionTypeBentoAvailable,
				Status:  metav1.ConditionUnknown,
				Reason:  "Reconciling",
				Message: "Model is seeding",
			},
		)
		if err != nil {
			return
		}
		return
	}

	bentoCR := &resourcesv1alpha1.Bento{
		ObjectMeta: metav1.ObjectMeta{
			Name:      bentoRequest.Name,
			Namespace: bentoRequest.Namespace,
		},
		Spec: resourcesv1alpha1.BentoSpec{
			Tag:         bentoRequest.Spec.BentoTag,
			Image:       imageInfo.ImageName,
			ServiceName: bentoRequest.Spec.ServiceName,
			Context:     bentoRequest.Spec.Context,
			Runners:     bentoRequest.Spec.Runners,
			Models:      bentoRequest.Spec.Models,
		},
	}

	if separateModels {
		bentoCR.Annotations = map[string]string{
			commonconsts.KubeAnnotationYataiImageBuilderSeparateModels: commonconsts.KubeLabelValueTrue,
			commonconsts.KubeAnnotationAWSAccessKeySecretName:          bentoRequest.Annotations[commonconsts.KubeAnnotationAWSAccessKeySecretName],
		}
		if isAddNamespacePrefix() { // deprecated
			bentoCR.Annotations[commonconsts.KubeAnnotationIsMultiTenancy] = commonconsts.KubeLabelValueTrue
		}
		bentoCR.Annotations[KubeAnnotationModelStorageNS] = bentoRequest.Annotations[KubeAnnotationModelStorageNS]
	}

	err = ctrl.SetControllerReference(bentoRequest, bentoCR, r.Scheme)
	if err != nil {
		err = errors.Wrap(err, "set controller reference")
		return
	}

	if imageInfo.DockerConfigJSONSecretName != "" {
		bentoCR.Spec.ImagePullSecrets = []corev1.LocalObjectReference{
			{
				Name: imageInfo.DockerConfigJSONSecretName,
			},
		}
	}

	if bentoRequest.Spec.DownloadURL == "" {
		var bento *schemasv1.BentoFullSchema
		bento, err = r.getBento(ctx, bentoRequest)
		if err != nil {
			err = errors.Wrap(err, "get bento")
			return
		}
		bentoCR.Spec.Context = &resourcesv1alpha1.BentoContext{
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

	bentoRequest, err = r.setStatusConditions(ctx, req,
		metav1.Condition{
			Type:    resourcesv1alpha1.BentoRequestConditionTypeBentoAvailable,
			Status:  metav1.ConditionTrue,
			Reason:  "Reconciling",
			Message: "Bento is generated",
		},
	)
	if err != nil {
		return
	}

	return
}

func isEstargzEnabled() bool {
	return os.Getenv("ESTARGZ_ENABLED") == commonconsts.KubeLabelValueTrue
}

type ensureImageExistsOption struct {
	bentoRequest *resourcesv1alpha1.BentoRequest
	req          ctrl.Request
}

func (r *BentoRequestReconciler) ensureImageExists(ctx context.Context, opt ensureImageExistsOption) (bentoRequest *resourcesv1alpha1.BentoRequest, imageInfo ImageInfo, imageExists bool, result ctrl.Result, err error) { // nolint: unparam
	logs := log.FromContext(ctx)

	bentoRequest = opt.bentoRequest
	req := opt.req

	imageInfo, err = r.getImageInfo(ctx, GetImageInfoOption{
		BentoRequest: bentoRequest,
	})
	if err != nil {
		err = errors.Wrap(err, "get image info")
		return
	}

	imageExistsCheckedCondition := meta.FindStatusCondition(bentoRequest.Status.Conditions, resourcesv1alpha1.BentoRequestConditionTypeImageExistsChecked)
	imageExistsCondition := meta.FindStatusCondition(bentoRequest.Status.Conditions, resourcesv1alpha1.BentoRequestConditionTypeImageExists)
	if imageExistsCheckedCondition == nil || imageExistsCheckedCondition.Status != metav1.ConditionTrue || imageExistsCheckedCondition.Message != imageInfo.ImageName {
		imageExistsCheckedCondition = &metav1.Condition{
			Type:    resourcesv1alpha1.BentoRequestConditionTypeImageExistsChecked,
			Status:  metav1.ConditionUnknown,
			Reason:  "Reconciling",
			Message: imageInfo.ImageName,
		}
		bentoAvailableCondition := &metav1.Condition{
			Type:    resourcesv1alpha1.BentoRequestConditionTypeBentoAvailable,
			Status:  metav1.ConditionUnknown,
			Reason:  "Reconciling",
			Message: "Checking image exists",
		}
		bentoRequest, err = r.setStatusConditions(ctx, req, *imageExistsCheckedCondition, *bentoAvailableCondition)
		if err != nil {
			return
		}
		r.Recorder.Eventf(bentoRequest, corev1.EventTypeNormal, "CheckingImage", "Checking image exists: %s", imageInfo.ImageName)
		imageExists, err = checkImageExists(bentoRequest, imageInfo.DockerRegistry, imageInfo.InClusterImageName)
		if err != nil {
			err = errors.Wrapf(err, "check image %s exists", imageInfo.ImageName)
			return
		}

		err = r.Get(ctx, req.NamespacedName, bentoRequest)
		if err != nil {
			logs.Error(err, "Failed to re-fetch BentoRequest")
			return
		}

		if imageExists {
			r.Recorder.Eventf(bentoRequest, corev1.EventTypeNormal, "CheckingImage", "Image exists: %s", imageInfo.ImageName)
			imageExistsCheckedCondition = &metav1.Condition{
				Type:    resourcesv1alpha1.BentoRequestConditionTypeImageExistsChecked,
				Status:  metav1.ConditionTrue,
				Reason:  "Reconciling",
				Message: imageInfo.ImageName,
			}
			imageExistsCondition = &metav1.Condition{
				Type:    resourcesv1alpha1.BentoRequestConditionTypeImageExists,
				Status:  metav1.ConditionTrue,
				Reason:  "Reconciling",
				Message: imageInfo.ImageName,
			}
			bentoRequest, err = r.setStatusConditions(ctx, req, *imageExistsCondition, *imageExistsCheckedCondition)
			if err != nil {
				return
			}
		} else {
			r.Recorder.Eventf(bentoRequest, corev1.EventTypeNormal, "CheckingImage", "Image not exists: %s", imageInfo.ImageName)
			imageExistsCheckedCondition = &metav1.Condition{
				Type:    resourcesv1alpha1.BentoRequestConditionTypeImageExistsChecked,
				Status:  metav1.ConditionFalse,
				Reason:  "Reconciling",
				Message: fmt.Sprintf("Image not exists: %s", imageInfo.ImageName),
			}
			imageExistsCondition = &metav1.Condition{
				Type:    resourcesv1alpha1.BentoRequestConditionTypeImageExists,
				Status:  metav1.ConditionFalse,
				Reason:  "Reconciling",
				Message: fmt.Sprintf("Image %s is not exists", imageInfo.ImageName),
			}
			bentoRequest, err = r.setStatusConditions(ctx, req, *imageExistsCondition, *imageExistsCheckedCondition)
			if err != nil {
				return
			}
		}
	}

	var bentoRequestHashStr string
	bentoRequestHashStr, err = r.getHashStr(bentoRequest)
	if err != nil {
		err = errors.Wrapf(err, "get BentoRequest %s/%s hash string", bentoRequest.Namespace, bentoRequest.Name)
		return
	}

	imageExists = imageExistsCondition != nil && imageExistsCondition.Status == metav1.ConditionTrue && imageExistsCondition.Message == imageInfo.ImageName
	if imageExists {
		return
	}

	jobLabels := map[string]string{
		commonconsts.KubeLabelBentoRequest:        bentoRequest.Name,
		commonconsts.KubeLabelIsBentoImageBuilder: commonconsts.KubeLabelValueTrue,
	}

	if isSeparateModels(opt.bentoRequest) {
		jobLabels[KubeLabelYataiImageBuilderSeparateModels] = commonconsts.KubeLabelValueTrue
	} else {
		jobLabels[KubeLabelYataiImageBuilderSeparateModels] = commonconsts.KubeLabelValueFalse
	}

	jobs := &batchv1.JobList{}
	err = r.List(ctx, jobs, client.InNamespace(req.Namespace), client.MatchingLabels(jobLabels))
	if err != nil {
		err = errors.Wrap(err, "list jobs")
		return
	}

	reservedJobs := make([]*batchv1.Job, 0)
	for _, job_ := range jobs.Items {
		job_ := job_

		oldHash := job_.Annotations[KubeAnnotationBentoRequestHash]
		if oldHash != bentoRequestHashStr {
			logs.Info("Because hash changed, delete old job", "job", job_.Name, "oldHash", oldHash, "newHash", bentoRequestHashStr)
			// --cascade=foreground
			err = r.Delete(ctx, &job_, &client.DeleteOptions{
				PropagationPolicy: &[]metav1.DeletionPropagation{metav1.DeletePropagationForeground}[0],
			})
			if err != nil {
				err = errors.Wrapf(err, "delete job %s", job_.Name)
				return
			}
			return
		} else {
			reservedJobs = append(reservedJobs, &job_)
		}
	}

	var job *batchv1.Job
	if len(reservedJobs) > 0 {
		job = reservedJobs[0]
	}

	if len(reservedJobs) > 1 {
		for _, job_ := range reservedJobs[1:] {
			logs.Info("Because has more than one job, delete old job", "job", job_.Name)
			// --cascade=foreground
			err = r.Delete(ctx, job_, &client.DeleteOptions{
				PropagationPolicy: &[]metav1.DeletionPropagation{metav1.DeletePropagationForeground}[0],
			})
			if err != nil {
				err = errors.Wrapf(err, "delete job %s", job_.Name)
				return
			}
		}
	}

	if job == nil {
		job, err = r.generateImageBuilderJob(ctx, GenerateImageBuilderJobOption{
			ImageInfo:    imageInfo,
			BentoRequest: bentoRequest,
		})
		if err != nil {
			err = errors.Wrap(err, "generate image builder job")
			return
		}
		r.Recorder.Eventf(bentoRequest, corev1.EventTypeNormal, "GenerateImageBuilderJob", "Creating image builder job: %s", job.Name)
		err = r.Create(ctx, job)
		if err != nil {
			err = errors.Wrapf(err, "create image builder job %s", job.Name)
			return
		}
		r.Recorder.Eventf(bentoRequest, corev1.EventTypeNormal, "GenerateImageBuilderJob", "Created image builder job: %s", job.Name)
		return
	}

	r.Recorder.Eventf(bentoRequest, corev1.EventTypeNormal, "CheckingImageBuilderJob", "Found image builder job: %s", job.Name)

	err = r.Get(ctx, req.NamespacedName, bentoRequest)
	if err != nil {
		logs.Error(err, "Failed to re-fetch BentoRequest")
		return
	}
	imageBuildingCondition := meta.FindStatusCondition(bentoRequest.Status.Conditions, resourcesv1alpha1.BentoRequestConditionTypeImageBuilding)

	isJobFailed := false
	isJobRunning := true

	if job.Spec.Completions != nil {
		if job.Status.Succeeded != *job.Spec.Completions {
			if job.Status.Failed > 0 {
				for _, condition := range job.Status.Conditions {
					if condition.Type == batchv1.JobFailed && condition.Status == corev1.ConditionTrue {
						isJobFailed = true
						break
					}
				}
			}
			isJobRunning = !isJobFailed
		} else {
			isJobRunning = false
		}
	}

	if isJobRunning {
		conditions := make([]metav1.Condition, 0)
		if job.Status.Active > 0 {
			conditions = append(conditions, metav1.Condition{
				Type:    resourcesv1alpha1.BentoRequestConditionTypeImageBuilding,
				Status:  metav1.ConditionTrue,
				Reason:  "Reconciling",
				Message: fmt.Sprintf("Image building job %s is running", job.Name),
			})
		} else {
			conditions = append(conditions, metav1.Condition{
				Type:    resourcesv1alpha1.BentoRequestConditionTypeImageBuilding,
				Status:  metav1.ConditionUnknown,
				Reason:  "Reconciling",
				Message: fmt.Sprintf("Image building job %s is waiting", job.Name),
			})
		}
		if bentoRequest.Spec.ImageBuildTimeout != nil {
			if imageBuildingCondition != nil && imageBuildingCondition.LastTransitionTime.Add(*bentoRequest.Spec.ImageBuildTimeout).Before(time.Now()) {
				conditions = append(conditions, metav1.Condition{
					Type:    resourcesv1alpha1.BentoRequestConditionTypeImageBuilding,
					Status:  metav1.ConditionFalse,
					Reason:  "Timeout",
					Message: fmt.Sprintf("Image building job %s is timeout", job.Name),
				})
				if _, err = r.setStatusConditions(ctx, req, conditions...); err != nil {
					return
				}
				err = errors.New("image build timeout")
				return
			}
		}

		if bentoRequest, err = r.setStatusConditions(ctx, req, conditions...); err != nil {
			return
		}

		if imageBuildingCondition != nil && imageBuildingCondition.Status != metav1.ConditionTrue && isJobRunning {
			r.Recorder.Eventf(bentoRequest, corev1.EventTypeNormal, "BentoImageBuilder", "Image is building now")
		}

		return
	}

	if isJobFailed {
		bentoRequest, err = r.setStatusConditions(ctx, req,
			metav1.Condition{
				Type:    resourcesv1alpha1.BentoRequestConditionTypeImageBuilding,
				Status:  metav1.ConditionFalse,
				Reason:  "Reconciling",
				Message: fmt.Sprintf("Image building job %s is failed.", job.Name),
			},
			metav1.Condition{
				Type:    resourcesv1alpha1.BentoRequestConditionTypeBentoAvailable,
				Status:  metav1.ConditionFalse,
				Reason:  "Reconciling",
				Message: fmt.Sprintf("Image building job %s is failed.", job.Name),
			},
		)
		if err != nil {
			return
		}
		return
	}

	bentoRequest, err = r.setStatusConditions(ctx, req,
		metav1.Condition{
			Type:    resourcesv1alpha1.BentoRequestConditionTypeImageBuilding,
			Status:  metav1.ConditionFalse,
			Reason:  "Reconciling",
			Message: fmt.Sprintf("Image building job %s is successed.", job.Name),
		},
		metav1.Condition{
			Type:    resourcesv1alpha1.BentoRequestConditionTypeImageExists,
			Status:  metav1.ConditionTrue,
			Reason:  "Reconciling",
			Message: imageInfo.ImageName,
		},
	)
	if err != nil {
		return
	}

	r.Recorder.Eventf(bentoRequest, corev1.EventTypeNormal, "BentoImageBuilder", "Image has been built successfully")

	imageExists = true

	return
}

type ensureModelsExistsOption struct {
	bentoRequest *resourcesv1alpha1.BentoRequest
	req          ctrl.Request
}

func (r *BentoRequestReconciler) ensureModelsExists(ctx context.Context, opt ensureModelsExistsOption) (bentoRequest *resourcesv1alpha1.BentoRequest, modelsExists bool, result ctrl.Result, err error) { // nolint: unparam
	bentoRequest = opt.bentoRequest
	modelTags := make([]string, 0)
	for _, model := range bentoRequest.Spec.Models {
		modelTags = append(modelTags, model.Tag)
	}

	modelsExistsCondition := meta.FindStatusCondition(bentoRequest.Status.Conditions, resourcesv1alpha1.BentoRequestConditionTypeModelsExists)
	r.Recorder.Eventf(bentoRequest, corev1.EventTypeNormal, "SeparateModels", "Separate models are enabled")
	if modelsExistsCondition == nil || modelsExistsCondition.Status == metav1.ConditionUnknown {
		r.Recorder.Eventf(bentoRequest, corev1.EventTypeNormal, "ModelsExists", "Models are not ready")
		modelsExistsCondition = &metav1.Condition{
			Type:    resourcesv1alpha1.BentoRequestConditionTypeModelsExists,
			Status:  metav1.ConditionFalse,
			Reason:  "Reconciling",
			Message: "Models are not ready",
		}
		bentoRequest, err = r.setStatusConditions(ctx, opt.req, *modelsExistsCondition)
		if err != nil {
			return
		}
	}

	modelsExists = modelsExistsCondition != nil && modelsExistsCondition.Status == metav1.ConditionTrue && modelsExistsCondition.Message == fmt.Sprintf("%s:%s", getJuiceFSStorageClassName(), strings.Join(modelTags, ", "))
	if modelsExists {
		return
	}

	modelsMap := make(map[string]*resourcesv1alpha1.BentoModel)
	for _, model := range bentoRequest.Spec.Models {
		model := model
		modelsMap[model.Tag] = &model
	}

	jobLabels := map[string]string{
		commonconsts.KubeLabelBentoRequest:  bentoRequest.Name,
		commonconsts.KubeLabelIsModelSeeder: "true",
	}

	jobs := &batchv1.JobList{}
	err = r.List(ctx, jobs, client.InNamespace(bentoRequest.Namespace), client.MatchingLabels(jobLabels))
	if err != nil {
		err = errors.Wrap(err, "list jobs")
		return
	}

	var bentoRequestHashStr string
	bentoRequestHashStr, err = r.getHashStr(bentoRequest)
	if err != nil {
		err = errors.Wrapf(err, "get BentoRequest %s/%s hash string", bentoRequest.Namespace, bentoRequest.Name)
		return
	}

	existingJobModelTags := make(map[string]struct{})
	for _, job_ := range jobs.Items {
		job_ := job_

		oldHash := job_.Annotations[KubeAnnotationBentoRequestHash]
		if oldHash != bentoRequestHashStr {
			r.Recorder.Eventf(bentoRequest, corev1.EventTypeNormal, "DeleteJob", "Because hash changed, delete old job %s, oldHash: %s, newHash: %s", job_.Name, oldHash, bentoRequestHashStr)
			// --cascade=foreground
			err = r.Delete(ctx, &job_, &client.DeleteOptions{
				PropagationPolicy: &[]metav1.DeletionPropagation{metav1.DeletePropagationForeground}[0],
			})
			if err != nil {
				err = errors.Wrapf(err, "delete job %s", job_.Name)
				return
			}
			continue
		}

		modelTag := fmt.Sprintf("%s:%s", job_.Labels[commonconsts.KubeLabelYataiModelRepository], job_.Labels[commonconsts.KubeLabelYataiModel])
		_, ok := modelsMap[modelTag]

		if !ok {
			r.Recorder.Eventf(bentoRequest, corev1.EventTypeNormal, "DeleteJob", "Due to the nonexistence of the model %s, job %s has been deleted.", modelTag, job_.Name)
			// --cascade=foreground
			err = r.Delete(ctx, &job_, &client.DeleteOptions{
				PropagationPolicy: &[]metav1.DeletionPropagation{metav1.DeletePropagationForeground}[0],
			})
			if err != nil {
				err = errors.Wrapf(err, "delete job %s", job_.Name)
				return
			}
		} else {
			existingJobModelTags[modelTag] = struct{}{}
		}
	}

	for _, model := range bentoRequest.Spec.Models {
		if _, ok := existingJobModelTags[model.Tag]; ok {
			continue
		}
		model := model
		pvc := &corev1.PersistentVolumeClaim{}
		pvcName := r.getModelPVCName(bentoRequest, &model)
		err = r.Get(ctx, client.ObjectKey{
			Namespace: bentoRequest.Namespace,
			Name:      pvcName,
		}, pvc)
		isPVCNotFound := k8serrors.IsNotFound(err)
		if err != nil && !isPVCNotFound {
			err = errors.Wrapf(err, "get PVC %s/%s", bentoRequest.Namespace, pvcName)
			return
		}
		if isPVCNotFound {
			pvc = r.generateModelPVC(GenerateModelPVCOption{
				BentoRequest: bentoRequest,
				Model:        &model,
			})
			err = r.Create(ctx, pvc)
			isPVCAlreadyExists := k8serrors.IsAlreadyExists(err)
			if err != nil && !isPVCAlreadyExists {
				err = errors.Wrapf(err, "create model %s/%s pvc", bentoRequest.Namespace, model.Tag)
				return
			}
		}
		var job *batchv1.Job
		job, err = r.generateModelSeederJob(ctx, GenerateModelSeederJobOption{
			BentoRequest: bentoRequest,
			Model:        &model,
		})
		if err != nil {
			err = errors.Wrap(err, "generate model seeder job")
			return
		}
		oldJob := &batchv1.Job{}
		err = r.Get(ctx, client.ObjectKeyFromObject(job), oldJob)
		oldJobIsNotFound := k8serrors.IsNotFound(err)
		if err != nil && !oldJobIsNotFound {
			err = errors.Wrap(err, "get job")
			return
		}
		if oldJobIsNotFound {
			err = r.Create(ctx, job)
			if err != nil {
				err = errors.Wrap(err, "create job")
				return
			}
			r.Recorder.Eventf(bentoRequest, corev1.EventTypeNormal, "CreateJob", "Job %s has been created.", job.Name)
		} else if !reflect.DeepEqual(job.Labels, oldJob.Labels) || !reflect.DeepEqual(job.Annotations, oldJob.Annotations) {
			job.Labels = oldJob.Labels
			job.Annotations = oldJob.Annotations
			err = r.Update(ctx, job)
			if err != nil {
				err = errors.Wrap(err, "update job")
				return
			}
			r.Recorder.Eventf(bentoRequest, corev1.EventTypeNormal, "UpdateJob", "Job %s has been updated.", job.Name)
		}
	}

	jobs = &batchv1.JobList{}
	err = r.List(ctx, jobs, client.InNamespace(bentoRequest.Namespace), client.MatchingLabels(jobLabels))
	if err != nil {
		err = errors.Wrap(err, "list jobs")
		return
	}

	succeedModelTags := make(map[string]struct{})
	failedJobNames := make([]string, 0)
	notReadyJobNames := make([]string, 0)
	for _, job_ := range jobs.Items {
		if job_.Spec.Completions != nil && job_.Status.Succeeded == *job_.Spec.Completions {
			modelTag := fmt.Sprintf("%s:%s", job_.Labels[commonconsts.KubeLabelYataiModelRepository], job_.Labels[commonconsts.KubeLabelYataiModel])
			succeedModelTags[modelTag] = struct{}{}
			continue
		}
		if job_.Status.Failed > 0 {
			failedJobNames = append(failedJobNames, job_.Name)
		}
		notReadyJobNames = append(notReadyJobNames, job_.Name)
	}

	if len(failedJobNames) > 0 {
		msg := fmt.Sprintf("Model seeder jobs failed: %s", strings.Join(failedJobNames, ", "))
		pods := &corev1.PodList{}
		err = r.List(ctx, pods, client.InNamespace(bentoRequest.Namespace), client.MatchingLabels(jobLabels))
		if err != nil {
			err = errors.Wrap(err, "list pods")
			return
		}
		hfValidateErr := false
		for _, pod_ := range pods.Items {
			for _, condition := range pod_.Status.Conditions {
				if condition.Type == corev1.PodInitialized && condition.Status == corev1.ConditionFalse {
					hfValidateErr = true
					break
				}
			}
		}
		if hfValidateErr {
			msg = fmt.Sprintf("%s: no validate HF_TOKEN for seeding huggingface model", msg)
		}
		r.Recorder.Event(bentoRequest, corev1.EventTypeNormal, "ModelsExists", msg)
		bentoRequest, err = r.setStatusConditions(ctx, opt.req,
			metav1.Condition{
				Type:    resourcesv1alpha1.BentoRequestConditionTypeModelsExists,
				Status:  metav1.ConditionFalse,
				Reason:  "Reconciling",
				Message: msg,
			},
			metav1.Condition{
				Type:    resourcesv1alpha1.BentoRequestConditionTypeBentoAvailable,
				Status:  metav1.ConditionFalse,
				Reason:  "Reconciling",
				Message: msg,
			},
		)
		if err != nil {
			return
		}
		err = errors.New(msg)
		return
	}

	modelsExists = true

	for _, model := range bentoRequest.Spec.Models {
		if _, ok := succeedModelTags[model.Tag]; !ok {
			modelsExists = false
			break
		}
	}

	if modelsExists {
		bentoRequest, err = r.setStatusConditions(ctx, opt.req,
			metav1.Condition{
				Type:    resourcesv1alpha1.BentoRequestConditionTypeModelsExists,
				Status:  metav1.ConditionTrue,
				Reason:  "Reconciling",
				Message: fmt.Sprintf("%s:%s", getJuiceFSStorageClassName(), strings.Join(modelTags, ", ")),
			},
			metav1.Condition{
				Type:    resourcesv1alpha1.BentoRequestConditionTypeModelsSeeding,
				Status:  metav1.ConditionFalse,
				Reason:  "Reconciling",
				Message: "All models have been seeded.",
			},
		)
		if err != nil {
			return
		}
	} else {
		bentoRequest, err = r.setStatusConditions(ctx, opt.req,
			metav1.Condition{
				Type:    resourcesv1alpha1.BentoRequestConditionTypeModelsSeeding,
				Status:  metav1.ConditionTrue,
				Reason:  "Reconciling",
				Message: fmt.Sprintf("Model seeder jobs are not ready: %s.", strings.Join(notReadyJobNames, ", ")),
			},
		)
		if err != nil {
			return
		}
	}
	return
}

func (r *BentoRequestReconciler) deleteImageBuilderJobs(ctx context.Context, bentoRequest *resourcesv1alpha1.BentoRequest) error {
	jobLabels := r.getImageBuilderJobLabels(bentoRequest)

	jobs := &batchv1.JobList{}
	if err := r.List(ctx, jobs, client.InNamespace(bentoRequest.Namespace), client.MatchingLabels(jobLabels)); err != nil {
		return errors.Wrap(err, "list image builder jobs")
	}

	for _, job := range jobs.Items {
		job := job
		if err := r.Delete(ctx, &job, client.PropagationPolicy(metav1.DeletePropagationBackground)); err != nil {
			return errors.Wrapf(err, "delete image builder job %s", job.Name)
		}
		r.Recorder.Eventf(bentoRequest, corev1.EventTypeNormal, "DeleteImageBuilderJob", "Deleted image builder job %s", job.Name)
	}

	return nil
}

func (r *BentoRequestReconciler) deleteModelSeederJobs(ctx context.Context, bentoRequest *resourcesv1alpha1.BentoRequest, model *resourcesv1alpha1.BentoModel) error {
	jobLabels := r.getModelSeederJobLabels(bentoRequest, model)

	jobs := &batchv1.JobList{}
	if err := r.List(ctx, jobs, client.InNamespace(bentoRequest.Namespace), client.MatchingLabels(jobLabels)); err != nil {
		return errors.Wrap(err, "list model seeder jobs")
	}

	for _, job := range jobs.Items {
		job := job
		if err := r.Delete(ctx, &job, client.PropagationPolicy(metav1.DeletePropagationBackground)); err != nil {
			return errors.Wrapf(err, "delete model seeder job %s", job.Name)
		}
		r.Recorder.Eventf(bentoRequest, corev1.EventTypeNormal, "DeleteModelSeederJob", "Deleted model seeder job %s", job.Name)
	}

	return nil
}

// deleteModelPVC deletes the PVC associated with a seeded model
func (r *BentoRequestReconciler) deleteModelPVC(ctx context.Context, bentoRequest *resourcesv1alpha1.BentoRequest, model *resourcesv1alpha1.BentoModel) error {
	pvcName := r.getModelPVCName(bentoRequest, model)
	pvc := &corev1.PersistentVolumeClaim{
		ObjectMeta: metav1.ObjectMeta{
			Name:      pvcName,
			Namespace: bentoRequest.Namespace,
		},
	}

	err := r.Delete(ctx, pvc)
	if err != nil {
		if k8serrors.IsNotFound(err) {
			// PVC already deleted, ignore the error
			return nil
		}
		return errors.Wrapf(err, "failed to delete PVC %s", pvcName)
	}

	r.Recorder.Eventf(bentoRequest, corev1.EventTypeNormal, "DeletePVC", "Successfully deleted PVC %s for model %s", pvcName, model.Tag)
	return nil
}

func (r *BentoRequestReconciler) setStatusConditions(ctx context.Context, req ctrl.Request, conditions ...metav1.Condition) (bentoRequest *resourcesv1alpha1.BentoRequest, err error) {
	bentoRequest = &resourcesv1alpha1.BentoRequest{}
	/*
		Please don't blame me when you see this kind of code,
		this is to avoid "the object has been modified; please apply your changes to the latest version and try again" when updating CR status,
		don't doubt that almost all CRD operators (e.g. cert-manager) can't avoid this stupid error and can only try to avoid this by this stupid way.
	*/
	for i := 0; i < 3; i++ {
		if err = r.Get(ctx, req.NamespacedName, bentoRequest); err != nil {
			err = errors.Wrap(err, "Failed to re-fetch BentoRequest")
			return
		}
		for _, condition := range conditions {
			meta.SetStatusCondition(&bentoRequest.Status.Conditions, condition)
		}
		if err = r.Status().Update(ctx, bentoRequest); err != nil {
			time.Sleep(100 * time.Millisecond)
		} else {
			break
		}
	}
	if err != nil {
		err = errors.Wrap(err, "Failed to update BentoRequest status")
		return
	}
	if err = r.Get(ctx, req.NamespacedName, bentoRequest); err != nil {
		err = errors.Wrap(err, "Failed to re-fetch BentoRequest")
		return
	}
	return
}

func UsingAWSECRWithIAMRole() bool {
	return os.Getenv(commonconsts.EnvAWSECRWithIAMRole) == commonconsts.KubeLabelValueTrue
}

func GetAWSECRRegion() string {
	return os.Getenv(commonconsts.EnvAWSECRRegion)
}

func CheckECRImageExists(imageName string) (bool, error) {
	region := GetAWSECRRegion()
	if region == "" {
		return false, fmt.Errorf("%s is not set", commonconsts.EnvAWSECRRegion)
	}

	sess, err := session.NewSession(&aws.Config{Region: aws.String(region)})
	if err != nil {
		return false, errors.Wrap(err, "create aws session")
	}

	_, _, imageName_ := xstrings.Partition(imageName, "/")
	repoName, _, tag := xstrings.Partition(imageName_, ":")

	svc := ecr.New(sess)
	input := &ecr.DescribeImagesInput{
		RepositoryName: aws.String(repoName),
		ImageIds: []*ecr.ImageIdentifier{
			{
				ImageTag: aws.String(tag),
			},
		},
	}

	_, err = svc.DescribeImages(input)
	if err != nil {
		var awsErr awserr.Error
		if errors.As(err, &awsErr) {
			if awsErr.Code() == "ImageNotFoundException" {
				return false, nil
			}
		}
		return false, errors.Wrap(err, "describe ECR images")
	}

	return true, nil
}

type BentoImageBuildEngine string

const (
	BentoImageBuildEngineKaniko           BentoImageBuildEngine = "kaniko"
	BentoImageBuildEngineBuildkit         BentoImageBuildEngine = "buildkit"
	BentoImageBuildEngineBuildkitRootless BentoImageBuildEngine = "buildkit-rootless"
)

const (
	EnvBentoImageBuildEngine = "BENTO_IMAGE_BUILD_ENGINE"
)

func getBentoImageBuildEngine() BentoImageBuildEngine {
	engine := os.Getenv(EnvBentoImageBuildEngine)
	if engine == "" {
		return BentoImageBuildEngineKaniko
	}
	return BentoImageBuildEngine(engine)
}

func (r *BentoRequestReconciler) makeSureDockerConfigJSONSecret(ctx context.Context, namespace string, dockerRegistryConf *commonconfig.DockerRegistryConfig) (dockerConfigJSONSecret *corev1.Secret, err error) {
	if dockerRegistryConf.Username == "" {
		return
	}

	// nolint: gosec
	dockerConfigSecretName := commonconsts.KubeSecretNameRegcred
	dockerConfigObj := struct {
		Auths map[string]struct {
			Auth string `json:"auth"`
		} `json:"auths"`
	}{
		Auths: map[string]struct {
			Auth string `json:"auth"`
		}{
			dockerRegistryConf.Server: {
				Auth: base64.StdEncoding.EncodeToString([]byte(fmt.Sprintf("%s:%s", dockerRegistryConf.Username, dockerRegistryConf.Password))),
			},
		},
	}

	dockerConfigContent, err := json.Marshal(dockerConfigObj)
	if err != nil {
		err = errors.Wrap(err, "marshal docker config")
		return nil, err
	}

	dockerConfigJSONSecret = &corev1.Secret{}

	err = r.Get(ctx, types.NamespacedName{Namespace: namespace, Name: dockerConfigSecretName}, dockerConfigJSONSecret)
	dockerConfigIsNotFound := k8serrors.IsNotFound(err)
	// nolint: gocritic
	if err != nil && !dockerConfigIsNotFound {
		err = errors.Wrap(err, "get docker config secret")
		return nil, err
	}
	err = nil
	if dockerConfigIsNotFound {
		dockerConfigJSONSecret = &corev1.Secret{
			Type: corev1.SecretTypeDockerConfigJson,
			ObjectMeta: metav1.ObjectMeta{
				Name:      dockerConfigSecretName,
				Namespace: namespace,
			},
			Data: map[string][]byte{
				".dockerconfigjson": dockerConfigContent,
			},
		}
		err_ := r.Create(ctx, dockerConfigJSONSecret)
		if err_ != nil {
			dockerConfigJSONSecret = &corev1.Secret{}
			err = r.Get(ctx, types.NamespacedName{Namespace: namespace, Name: dockerConfigSecretName}, dockerConfigJSONSecret)
			dockerConfigIsNotFound = k8serrors.IsNotFound(err)
			if err != nil && !dockerConfigIsNotFound {
				err = errors.Wrap(err, "get docker config secret")
				return nil, err
			}
			if dockerConfigIsNotFound {
				err_ = errors.Wrap(err_, "create docker config secret")
				return nil, err_
			}
			if err != nil {
				err = nil
			}
		}
	} else {
		dockerConfigJSONSecret.Data[".dockerconfigjson"] = dockerConfigContent
		err = r.Update(ctx, dockerConfigJSONSecret)
		if err != nil {
			err = errors.Wrap(err, "update docker config secret")
			return nil, err
		}
	}

	return
}

func (r *BentoRequestReconciler) getYataiClient(ctx context.Context) (yataiClient **yataiclient.YataiClient, yataiConf **commonconfig.YataiConfig, err error) {
	yataiConf_, err := commonconfig.GetYataiConfig(ctx, func(ctx context.Context, namespace, name string) (*corev1.Secret, error) {
		secret := &corev1.Secret{}
		err := r.Get(ctx, types.NamespacedName{
			Namespace: namespace,
			Name:      name,
		}, secret)
		return secret, errors.Wrap(err, "get secret")
	}, commonconsts.YataiImageBuilderComponentName, true)
	isNotFound := k8serrors.IsNotFound(err)
	if err != nil && !isNotFound {
		err = errors.Wrap(err, "get yatai config")
		return
	}

	if isNotFound {
		return
	}

	if yataiConf_.Endpoint == "" {
		return
	}

	if yataiConf_.ClusterName == "" {
		yataiConf_.ClusterName = "default"
	}

	yataiClient_ := yataiclient.NewYataiClient(yataiConf_.Endpoint, fmt.Sprintf("%s:%s:%s", commonconsts.YataiImageBuilderComponentName, yataiConf_.ClusterName, yataiConf_.ApiToken))

	yataiClient = &yataiClient_
	yataiConf = &yataiConf_
	return
}

func (r *BentoRequestReconciler) getDockerRegistry(ctx context.Context, bentoRequest *resourcesv1alpha1.BentoRequest) (dockerRegistry modelschemas.DockerRegistrySchema, err error) {
	if bentoRequest != nil && bentoRequest.Spec.DockerConfigJSONSecretName != "" {
		secret := &corev1.Secret{}
		err = r.Get(ctx, types.NamespacedName{
			Namespace: bentoRequest.Namespace,
			Name:      bentoRequest.Spec.DockerConfigJSONSecretName,
		}, secret)
		if err != nil {
			err = errors.Wrapf(err, "get docker config json secret %s", bentoRequest.Spec.DockerConfigJSONSecretName)
			return
		}
		configJSON, ok := secret.Data[".dockerconfigjson"]
		if !ok {
			err = errors.Errorf("docker config json secret %s does not have .dockerconfigjson key", bentoRequest.Spec.DockerConfigJSONSecretName)
			return
		}
		var configObj struct {
			Auths map[string]struct {
				Auth string `json:"auth"`
			} `json:"auths"`
		}
		err = json.Unmarshal(configJSON, &configObj)
		if err != nil {
			err = errors.Wrapf(err, "unmarshal docker config json secret %s", bentoRequest.Spec.DockerConfigJSONSecretName)
			return
		}
		imageRegistryURI, _, _ := xstrings.Partition(bentoRequest.Spec.Image, "/")
		var server string
		var auth string
		if imageRegistryURI != "" {
			for k, v := range configObj.Auths {
				if k == imageRegistryURI {
					server = k
					auth = v.Auth
					break
				}
			}
			if server == "" {
				for k, v := range configObj.Auths {
					if strings.Contains(k, imageRegistryURI) {
						server = k
						auth = v.Auth
						break
					}
				}
			}
		}
		if server == "" {
			for k, v := range configObj.Auths {
				server = k
				auth = v.Auth
				break
			}
		}
		if server == "" {
			err = errors.Errorf("no auth in docker config json secret %s", bentoRequest.Spec.DockerConfigJSONSecretName)
			return
		}
		dockerRegistry.Server = server
		var credentials []byte
		credentials, err = base64.StdEncoding.DecodeString(auth)
		if err != nil {
			err = errors.Wrapf(err, "cannot base64 decode auth in docker config json secret %s", bentoRequest.Spec.DockerConfigJSONSecretName)
			return
		}
		dockerRegistry.Username, _, dockerRegistry.Password = xstrings.Partition(string(credentials), ":")
		if bentoRequest.Spec.OCIRegistryInsecure != nil {
			dockerRegistry.Secure = !*bentoRequest.Spec.OCIRegistryInsecure
		}
		return
	}

	dockerRegistryConfig, err := commonconfig.GetDockerRegistryConfig(ctx, func(ctx context.Context, namespace, name string) (*corev1.Secret, error) {
		secret := &corev1.Secret{}
		err := r.Get(ctx, types.NamespacedName{
			Namespace: namespace,
			Name:      name,
		}, secret)
		return secret, errors.Wrap(err, "get secret")
	})
	if err != nil {
		err = errors.Wrap(err, "get docker registry")
		return
	}

	bentoRepositoryName := "yatai-bentos"
	modelRepositoryName := "yatai-models"
	if dockerRegistryConfig.BentoRepositoryName != "" {
		bentoRepositoryName = dockerRegistryConfig.BentoRepositoryName
	}
	if dockerRegistryConfig.ModelRepositoryName != "" {
		modelRepositoryName = dockerRegistryConfig.ModelRepositoryName
	}
	bentoRepositoryURI := fmt.Sprintf("%s/%s", strings.TrimRight(dockerRegistryConfig.Server, "/"), bentoRepositoryName)
	modelRepositoryURI := fmt.Sprintf("%s/%s", strings.TrimRight(dockerRegistryConfig.Server, "/"), modelRepositoryName)
	if strings.Contains(dockerRegistryConfig.Server, "docker.io") {
		bentoRepositoryURI = fmt.Sprintf("docker.io/%s", bentoRepositoryName)
		modelRepositoryURI = fmt.Sprintf("docker.io/%s", modelRepositoryName)
	}
	bentoRepositoryInClusterURI := bentoRepositoryURI
	modelRepositoryInClusterURI := modelRepositoryURI
	if dockerRegistryConfig.InClusterServer != "" {
		bentoRepositoryInClusterURI = fmt.Sprintf("%s/%s", strings.TrimRight(dockerRegistryConfig.InClusterServer, "/"), bentoRepositoryName)
		modelRepositoryInClusterURI = fmt.Sprintf("%s/%s", strings.TrimRight(dockerRegistryConfig.InClusterServer, "/"), modelRepositoryName)
		if strings.Contains(dockerRegistryConfig.InClusterServer, "docker.io") {
			bentoRepositoryInClusterURI = fmt.Sprintf("docker.io/%s", bentoRepositoryName)
			modelRepositoryInClusterURI = fmt.Sprintf("docker.io/%s", modelRepositoryName)
		}
	}
	dockerRegistry = modelschemas.DockerRegistrySchema{
		Server:                       dockerRegistryConfig.Server,
		Username:                     dockerRegistryConfig.Username,
		Password:                     dockerRegistryConfig.Password,
		Secure:                       dockerRegistryConfig.Secure,
		BentosRepositoryURI:          bentoRepositoryURI,
		BentosRepositoryURIInCluster: bentoRepositoryInClusterURI,
		ModelsRepositoryURI:          modelRepositoryURI,
		ModelsRepositoryURIInCluster: modelRepositoryInClusterURI,
	}

	return
}

func isAddNamespacePrefix() bool {
	return os.Getenv("ADD_NAMESPACE_PREFIX_TO_IMAGE_NAME") == trueStr
}

func getBentoImagePrefix(bentoRequest *resourcesv1alpha1.BentoRequest) string {
	if bentoRequest == nil {
		return ""
	}
	prefix, exist := bentoRequest.Annotations[KubeAnnotationBentoStorageNS]
	if exist && prefix != "" {
		return fmt.Sprintf("%s.", prefix)
	}
	if isAddNamespacePrefix() {
		return fmt.Sprintf("%s.", bentoRequest.Namespace)
	}
	return ""
}

func getModelNamespace(bentoRequest *resourcesv1alpha1.BentoRequest) string {
	if bentoRequest == nil {
		return ""
	}
	prefix := bentoRequest.Annotations[KubeAnnotationModelStorageNS]
	if prefix != "" {
		return prefix
	}
	if isAddNamespacePrefix() {
		return bentoRequest.Namespace
	}
	return ""
}

func getBentoImageName(bentoRequest *resourcesv1alpha1.BentoRequest, dockerRegistry modelschemas.DockerRegistrySchema, bentoRepositoryName, bentoVersion string, inCluster bool) string {
	if bentoRequest != nil && bentoRequest.Spec.Image != "" {
		return bentoRequest.Spec.Image
	}
	var uri, tag string
	if inCluster {
		uri = dockerRegistry.BentosRepositoryURIInCluster
	} else {
		uri = dockerRegistry.BentosRepositoryURI
	}
	tail := fmt.Sprintf("%s.%s", bentoRepositoryName, bentoVersion)
	separateModels := isSeparateModels(bentoRequest)
	if separateModels {
		tail += ".nomodels"
	}
	if isEstargzEnabled() {
		tail += ".esgz"
	}

	tag = fmt.Sprintf("yatai.%s%s", getBentoImagePrefix(bentoRequest), tail)

	if len(tag) > 128 {
		hashStr := hash(tail)
		tag = fmt.Sprintf("yatai.%s%s", getBentoImagePrefix(bentoRequest), hashStr)
		if len(tag) > 128 {
			tag = fmt.Sprintf("yatai.%s", hash(fmt.Sprintf("%s%s", getBentoImagePrefix(bentoRequest), tail)))[:128]
		}
	}
	return fmt.Sprintf("%s:%s", uri, tag)
}

func isSeparateModels(bentoRequest *resourcesv1alpha1.BentoRequest) (separateModels bool) {
	return bentoRequest.Annotations[commonconsts.KubeAnnotationYataiImageBuilderSeparateModels] == commonconsts.KubeLabelValueTrue
}

func checkImageExists(bentoRequest *resourcesv1alpha1.BentoRequest, dockerRegistry modelschemas.DockerRegistrySchema, imageName string) (bool, error) {
	if bentoRequest.Annotations["yatai.ai/force-build-image"] == commonconsts.KubeLabelValueTrue {
		return false, nil
	}

	if UsingAWSECRWithIAMRole() {
		return CheckECRImageExists(imageName)
	}

	server, _, imageName := xstrings.Partition(imageName, "/")
	if strings.Contains(server, "docker.io") {
		server = "index.docker.io"
	}
	if dockerRegistry.Secure {
		server = fmt.Sprintf("https://%s", server)
	} else {
		server = fmt.Sprintf("http://%s", server)
	}
	hub, err := registry.New(server, dockerRegistry.Username, dockerRegistry.Password, logrus.Debugf)
	if err != nil {
		err = errors.Wrapf(err, "create docker registry client for %s", server)
		return false, err
	}
	imageName, _, tag := xstrings.LastPartition(imageName, ":")
	tags, err := hub.Tags(imageName)
	isNotFound := err != nil && strings.Contains(err.Error(), "404")
	if isNotFound {
		return false, nil
	}
	if err != nil {
		err = errors.Wrapf(err, "get tags for docker image %s", imageName)
		return false, err
	}
	for _, tag_ := range tags {
		if tag_ == tag {
			return true, nil
		}
	}
	return false, nil
}

type ImageInfo struct {
	DockerRegistry             modelschemas.DockerRegistrySchema
	DockerConfigJSONSecretName string
	ImageName                  string
	InClusterImageName         string
	DockerRegistryInsecure     bool
}

type GetImageInfoOption struct {
	BentoRequest *resourcesv1alpha1.BentoRequest
}

func (r *BentoRequestReconciler) getImageInfo(ctx context.Context, opt GetImageInfoOption) (imageInfo ImageInfo, err error) {
	bentoRepositoryName, _, bentoVersion := xstrings.Partition(opt.BentoRequest.Spec.BentoTag, ":")
	dockerRegistry, err := r.getDockerRegistry(ctx, opt.BentoRequest)
	if err != nil {
		err = errors.Wrap(err, "get docker registry")
		return
	}
	imageInfo.DockerRegistry = dockerRegistry
	imageInfo.ImageName = getBentoImageName(opt.BentoRequest, dockerRegistry, bentoRepositoryName, bentoVersion, false)
	imageInfo.InClusterImageName = getBentoImageName(opt.BentoRequest, dockerRegistry, bentoRepositoryName, bentoVersion, true)

	imageInfo.DockerConfigJSONSecretName = opt.BentoRequest.Spec.DockerConfigJSONSecretName

	imageInfo.DockerRegistryInsecure = opt.BentoRequest.Annotations[commonconsts.KubeAnnotationDockerRegistryInsecure] == "true"
	if opt.BentoRequest.Spec.OCIRegistryInsecure != nil {
		imageInfo.DockerRegistryInsecure = *opt.BentoRequest.Spec.OCIRegistryInsecure
	}

	if imageInfo.DockerConfigJSONSecretName == "" {
		var dockerRegistryConf *commonconfig.DockerRegistryConfig
		dockerRegistryConf, err = commonconfig.GetDockerRegistryConfig(ctx, func(ctx context.Context, namespace, name string) (*corev1.Secret, error) {
			secret := &corev1.Secret{}
			err := r.Get(ctx, types.NamespacedName{Namespace: namespace, Name: name}, secret)
			return secret, errors.Wrap(err, "get docker registry secret")
		})
		if err != nil {
			err = errors.Wrap(err, "get docker registry")
			return
		}
		imageInfo.DockerRegistryInsecure = !dockerRegistryConf.Secure
		var dockerConfigSecret *corev1.Secret
		r.Recorder.Eventf(opt.BentoRequest, corev1.EventTypeNormal, "GenerateImageBuilderPod", "Making sure docker config secret %s in namespace %s", commonconsts.KubeSecretNameRegcred, opt.BentoRequest.Namespace)
		dockerConfigSecret, err = r.makeSureDockerConfigJSONSecret(ctx, opt.BentoRequest.Namespace, dockerRegistryConf)
		if err != nil {
			err = errors.Wrap(err, "make sure docker config secret")
			return
		}
		r.Recorder.Eventf(opt.BentoRequest, corev1.EventTypeNormal, "GenerateImageBuilderPod", "Docker config secret %s in namespace %s is ready", commonconsts.KubeSecretNameRegcred, opt.BentoRequest.Namespace)
		if dockerConfigSecret != nil {
			imageInfo.DockerConfigJSONSecretName = dockerConfigSecret.Name
		}
	}
	return
}

func (r *BentoRequestReconciler) getBento(ctx context.Context, bentoRequest *resourcesv1alpha1.BentoRequest) (bento *schemasv1.BentoFullSchema, err error) {
	bentoRepositoryName, _, bentoVersion := xstrings.Partition(bentoRequest.Spec.BentoTag, ":")

	yataiClient_, _, err := r.getYataiClient(ctx)
	if err != nil {
		err = errors.Wrap(err, "get yatai client")
		return
	}

	if yataiClient_ == nil {
		err = errors.New("can't get yatai client, please check yatai configuration")
		return
	}

	yataiClient := *yataiClient_

	r.Recorder.Eventf(bentoRequest, corev1.EventTypeNormal, "FetchBento", "Getting bento %s from yatai service", bentoRequest.Spec.BentoTag)
	bento, err = yataiClient.GetBento(ctx, bentoRepositoryName, bentoVersion)
	if err != nil {
		err = errors.Wrap(err, "get bento")
		return
	}
	r.Recorder.Eventf(bentoRequest, corev1.EventTypeNormal, "FetchBento", "Got bento %s from yatai service", bentoRequest.Spec.BentoTag)
	return
}

func (r *BentoRequestReconciler) getImageBuilderJobName() string {
	guid := xid.New()
	return fmt.Sprintf("yatai-bento-image-builder-%s", guid.String())
}

func (r *BentoRequestReconciler) getModelSeederJobName() string {
	guid := xid.New()
	return fmt.Sprintf("yatai-model-seeder-%s", guid.String())
}

func (r *BentoRequestReconciler) getModelSeederJobLabels(bentoRequest *resourcesv1alpha1.BentoRequest, model *resourcesv1alpha1.BentoModel) map[string]string {
	bentoRepositoryName, _, bentoVersion := xstrings.Partition(bentoRequest.Spec.BentoTag, ":")
	modelRepositoryName, _, modelVersion := xstrings.Partition(model.Tag, ":")
	return map[string]string{
		commonconsts.KubeLabelBentoRequest:         bentoRequest.Name,
		commonconsts.KubeLabelIsModelSeeder:        "true",
		commonconsts.KubeLabelYataiModelRepository: modelRepositoryName,
		commonconsts.KubeLabelYataiModel:           modelVersion,
		commonconsts.KubeLabelYataiBentoRepository: bentoRepositoryName,
		commonconsts.KubeLabelYataiBento:           bentoVersion,
	}
}

func (r *BentoRequestReconciler) getModelSeederPodLabels(bentoRequest *resourcesv1alpha1.BentoRequest, model *resourcesv1alpha1.BentoModel) map[string]string {
	bentoRepositoryName, _, bentoVersion := xstrings.Partition(bentoRequest.Spec.BentoTag, ":")
	modelRepositoryName, _, modelVersion := xstrings.Partition(model.Tag, ":")
	return map[string]string{
		commonconsts.KubeLabelBentoRequest:         bentoRequest.Name,
		commonconsts.KubeLabelIsModelSeeder:        "true",
		commonconsts.KubeLabelIsBentoImageBuilder:  "true",
		commonconsts.KubeLabelYataiModelRepository: modelRepositoryName,
		commonconsts.KubeLabelYataiModel:           modelVersion,
		commonconsts.KubeLabelYataiBentoRepository: bentoRepositoryName,
		commonconsts.KubeLabelYataiBento:           bentoVersion,
	}
}

func (r *BentoRequestReconciler) getImageBuilderJobLabels(bentoRequest *resourcesv1alpha1.BentoRequest) map[string]string {
	bentoRepositoryName, _, bentoVersion := xstrings.Partition(bentoRequest.Spec.BentoTag, ":")
	labels := map[string]string{
		commonconsts.KubeLabelBentoRequest:         bentoRequest.Name,
		commonconsts.KubeLabelIsBentoImageBuilder:  "true",
		commonconsts.KubeLabelYataiBentoRepository: bentoRepositoryName,
		commonconsts.KubeLabelYataiBento:           bentoVersion,
	}

	if isSeparateModels(bentoRequest) {
		labels[KubeLabelYataiImageBuilderSeparateModels] = commonconsts.KubeLabelValueTrue
	} else {
		labels[KubeLabelYataiImageBuilderSeparateModels] = commonconsts.KubeLabelValueFalse
	}
	return labels
}

func (r *BentoRequestReconciler) getImageBuilderPodLabels(bentoRequest *resourcesv1alpha1.BentoRequest) map[string]string {
	bentoRepositoryName, _, bentoVersion := xstrings.Partition(bentoRequest.Spec.BentoTag, ":")
	return map[string]string{
		commonconsts.KubeLabelBentoRequest:         bentoRequest.Name,
		commonconsts.KubeLabelIsBentoImageBuilder:  "true",
		commonconsts.KubeLabelYataiBentoRepository: bentoRepositoryName,
		commonconsts.KubeLabelYataiBento:           bentoVersion,
	}
}

func hash(text string) string {
	// nolint: gosec
	hasher := md5.New()
	hasher.Write([]byte(text))
	return hex.EncodeToString(hasher.Sum(nil))
}

func (r *BentoRequestReconciler) getModelPVCName(bentoRequest *resourcesv1alpha1.BentoRequest, model *resourcesv1alpha1.BentoModel) string {
	storageClassName := getJuiceFSStorageClassName()
	var hashStr string
	ns := getModelNamespace(bentoRequest)
	if ns == "" {
		hashStr = hash(fmt.Sprintf("%s:%s", storageClassName, model.Tag))
	} else {
		hashStr = hash(fmt.Sprintf("%s:%s:%s", storageClassName, ns, model.Tag))
	}
	pvcName := fmt.Sprintf("model-seeder-%s", hashStr)
	if len(pvcName) > 63 {
		pvcName = pvcName[:63]
	}
	return pvcName
}

func (r *BentoRequestReconciler) getJuiceFSModelPath(bentoRequest *resourcesv1alpha1.BentoRequest, model *resourcesv1alpha1.BentoModel) string {
	modelRepositoryName, _, modelVersion := xstrings.Partition(model.Tag, ":")
	ns := getModelNamespace(bentoRequest)
	if isHuggingfaceModel(model) {
		modelVersion = "all"
	}
	var path string
	if ns == "" {
		path = fmt.Sprintf("models/.shared/%s/%s", modelRepositoryName, modelVersion)
	} else {
		path = fmt.Sprintf("models/%s/%s/%s", ns, modelRepositoryName, modelVersion)
	}
	return path
}

func isHuggingfaceModel(model *resourcesv1alpha1.BentoModel) bool {
	return strings.HasPrefix(model.DownloadURL, "hf://")
}

type GenerateModelPVCOption struct {
	BentoRequest *resourcesv1alpha1.BentoRequest
	Model        *resourcesv1alpha1.BentoModel
}

func (r *BentoRequestReconciler) generateModelPVC(opt GenerateModelPVCOption) (pvc *corev1.PersistentVolumeClaim) {
	modelRepositoryName, _, modelVersion := xstrings.Partition(opt.Model.Tag, ":")
	storageSize := resource.MustParse("100Gi")
	if opt.Model.Size != nil {
		storageSize = *opt.Model.Size
		minStorageSize := resource.MustParse("1Gi")
		if storageSize.Value() < minStorageSize.Value() {
			storageSize = minStorageSize
		}
		storageSize.Set(storageSize.Value() * 2)
	}
	path := r.getJuiceFSModelPath(opt.BentoRequest, opt.Model)
	pvcName := r.getModelPVCName(opt.BentoRequest, opt.Model)
	pvc = &corev1.PersistentVolumeClaim{
		ObjectMeta: metav1.ObjectMeta{
			Name:      pvcName,
			Namespace: opt.BentoRequest.Namespace,
			Annotations: map[string]string{
				"path": path,
			},
			Labels: map[string]string{
				commonconsts.KubeLabelYataiModelRepository: modelRepositoryName,
				commonconsts.KubeLabelYataiModel:           modelVersion,
			},
		},
		Spec: corev1.PersistentVolumeClaimSpec{
			AccessModes: []corev1.PersistentVolumeAccessMode{corev1.ReadWriteMany},
			Resources: corev1.ResourceRequirements{
				Requests: corev1.ResourceList{
					corev1.ResourceStorage: storageSize,
				},
			},
			StorageClassName: pointer.StringPtr(getJuiceFSStorageClassName()),
		},
	}
	return
}

type GenerateModelSeederJobOption struct {
	BentoRequest *resourcesv1alpha1.BentoRequest
	Model        *resourcesv1alpha1.BentoModel
}

func (r *BentoRequestReconciler) generateModelSeederJob(ctx context.Context, opt GenerateModelSeederJobOption) (job *batchv1.Job, err error) {
	// nolint: gosimple
	podTemplateSpec, err := r.generateModelSeederPodTemplateSpec(ctx, GenerateModelSeederPodTemplateSpecOption{
		BentoRequest: opt.BentoRequest,
		Model:        opt.Model,
	})
	if err != nil {
		err = errors.Wrap(err, "generate model seeder pod template spec")
		return
	}
	kubeAnnotations := make(map[string]string)
	hashStr, err := r.getHashStr(opt.BentoRequest)
	if err != nil {
		err = errors.Wrap(err, "failed to get hash string")
		return
	}
	kubeAnnotations[KubeAnnotationBentoRequestHash] = hashStr
	job = &batchv1.Job{
		ObjectMeta: metav1.ObjectMeta{
			Name:        r.getModelSeederJobName(),
			Namespace:   opt.BentoRequest.Namespace,
			Labels:      r.getModelSeederJobLabels(opt.BentoRequest, opt.Model),
			Annotations: kubeAnnotations,
		},
		Spec: batchv1.JobSpec{
			Completions: pointer.Int32Ptr(1),
			Parallelism: pointer.Int32Ptr(1),
			PodFailurePolicy: &batchv1.PodFailurePolicy{
				Rules: []batchv1.PodFailurePolicyRule{
					{
						Action: batchv1.PodFailurePolicyActionFailJob,
						OnExitCodes: &batchv1.PodFailurePolicyOnExitCodesRequirement{
							ContainerName: pointer.StringPtr(ModelSeederContainerName),
							Operator:      batchv1.PodFailurePolicyOnExitCodesOpIn,
							Values:        []int32{ModelSeederJobFailedExitCode},
						},
					},
				},
			},
			Template: *podTemplateSpec,
		},
	}
	err = ctrl.SetControllerReference(opt.BentoRequest, job, r.Scheme)
	if err != nil {
		err = errors.Wrapf(err, "set controller reference for job %s", job.Name)
		return
	}
	return
}

type GenerateModelSeederPodTemplateSpecOption struct {
	BentoRequest *resourcesv1alpha1.BentoRequest
	Model        *resourcesv1alpha1.BentoModel
}

func (r *BentoRequestReconciler) generateModelSeederPodTemplateSpec(ctx context.Context, opt GenerateModelSeederPodTemplateSpecOption) (pod *corev1.PodTemplateSpec, err error) {
	kubeLabels := r.getModelSeederPodLabels(opt.BentoRequest, opt.Model)

	volumes := make([]corev1.Volume, 0)

	volumeMounts := make([]corev1.VolumeMount, 0)

	yataiAPITokenSecretName := ""

	// nolint: gosec
	awsAccessKeySecretName := opt.BentoRequest.Annotations[commonconsts.KubeAnnotationAWSAccessKeySecretName]
	if awsAccessKeySecretName == "" {
		awsAccessKeyID := os.Getenv(commonconsts.EnvAWSAccessKeyID)
		awsSecretAccessKey := os.Getenv(commonconsts.EnvAWSSecretAccessKey)
		if awsAccessKeyID != "" && awsSecretAccessKey != "" {
			// nolint: gosec
			awsAccessKeySecretName = YataiImageBuilderAWSAccessKeySecretName
			stringData := map[string]string{
				commonconsts.EnvAWSAccessKeyID:     awsAccessKeyID,
				commonconsts.EnvAWSSecretAccessKey: awsSecretAccessKey,
			}
			awsAccessKeySecret := &corev1.Secret{
				ObjectMeta: metav1.ObjectMeta{
					Name:      awsAccessKeySecretName,
					Namespace: opt.BentoRequest.Namespace,
				},
				StringData: stringData,
			}
			r.Recorder.Eventf(opt.BentoRequest, corev1.EventTypeNormal, "GenerateImageBuilderPod", "Creating or updating secret %s in namespace %s", awsAccessKeySecretName, opt.BentoRequest.Namespace)
			_, err = controllerutil.CreateOrUpdate(ctx, r.Client, awsAccessKeySecret, func() error {
				awsAccessKeySecret.StringData = stringData
				return nil
			})
			if err != nil {
				err = errors.Wrapf(err, "failed to create or update secret %s", awsAccessKeySecretName)
				return
			}
			r.Recorder.Eventf(opt.BentoRequest, corev1.EventTypeNormal, "GenerateImageBuilderPod", "Secret %s is created or updated in namespace %s", awsAccessKeySecretName, opt.BentoRequest.Namespace)
		}
	}

	internalImages := commonconfig.GetInternalImages()
	logrus.Infof("Model seeder is using the images %v", *internalImages)

	downloaderContainerResources := corev1.ResourceRequirements{
		Limits: corev1.ResourceList{
			corev1.ResourceCPU:    resource.MustParse("1000m"),
			corev1.ResourceMemory: resource.MustParse("3000Mi"),
		},
		Requests: corev1.ResourceList{
			corev1.ResourceCPU:    resource.MustParse("100m"),
			corev1.ResourceMemory: resource.MustParse("1000Mi"),
		},
	}

	downloaderContainerEnvFrom := opt.BentoRequest.Spec.DownloaderContainerEnvFrom

	if yataiAPITokenSecretName != "" {
		downloaderContainerEnvFrom = append(downloaderContainerEnvFrom, corev1.EnvFromSource{
			SecretRef: &corev1.SecretEnvSource{
				LocalObjectReference: corev1.LocalObjectReference{
					Name: yataiAPITokenSecretName,
				},
			},
		})
	}

	if awsAccessKeySecretName != "" {
		downloaderContainerEnvFrom = append(downloaderContainerEnvFrom, corev1.EnvFromSource{
			SecretRef: &corev1.SecretEnvSource{
				LocalObjectReference: corev1.LocalObjectReference{
					Name: awsAccessKeySecretName,
				},
			},
		})
	}

	containers := make([]corev1.Container, 0)

	model := opt.Model
	modelRepositoryName, _, modelVersion := xstrings.Partition(model.Tag, ":")
	modelDownloadURL := model.DownloadURL
	modelDownloadHeader := ""
	if modelDownloadURL == "" {
		var yataiClient_ **yataiclient.YataiClient
		var yataiConf_ **commonconfig.YataiConfig

		yataiClient_, yataiConf_, err = r.getYataiClient(ctx)
		if err != nil {
			err = errors.Wrap(err, "get yatai client")
			return
		}

		if yataiClient_ == nil || yataiConf_ == nil {
			err = errors.New("can't get yatai client, please check yatai configuration")
			return
		}

		yataiClient := *yataiClient_
		yataiConf := *yataiConf_

		var model_ *schemasv1.ModelFullSchema
		r.Recorder.Eventf(opt.BentoRequest, corev1.EventTypeNormal, "GenerateImageBuilderPod", "Getting model %s from yatai service", model.Tag)
		model_, err = yataiClient.GetModel(ctx, modelRepositoryName, modelVersion)
		if err != nil {
			err = errors.Wrap(err, "get model")
			return
		}
		r.Recorder.Eventf(opt.BentoRequest, corev1.EventTypeNormal, "GenerateImageBuilderPod", "Model %s is got from yatai service", model.Tag)

		if model_.TransmissionStrategy != nil && *model_.TransmissionStrategy == modelschemas.TransmissionStrategyPresignedURL {
			var model0 *schemasv1.ModelSchema
			r.Recorder.Eventf(opt.BentoRequest, corev1.EventTypeNormal, "GenerateImageBuilderPod", "Getting presigned url for model %s from yatai service", model.Tag)
			model0, err = yataiClient.PresignModelDownloadURL(ctx, modelRepositoryName, modelVersion)
			if err != nil {
				err = errors.Wrap(err, "presign model download url")
				return
			}
			r.Recorder.Eventf(opt.BentoRequest, corev1.EventTypeNormal, "GenerateImageBuilderPod", "Presigned url for model %s is got from yatai service", model.Tag)
			modelDownloadURL = model0.PresignedDownloadUrl
		} else {
			modelDownloadURL = fmt.Sprintf("%s/api/v1/model_repositories/%s/models/%s/download", yataiConf.Endpoint, modelRepositoryName, modelVersion)
			modelDownloadHeader = fmt.Sprintf("%s: %s:%s:$%s", commonconsts.YataiApiTokenHeaderName, commonconsts.YataiImageBuilderComponentName, yataiConf.ClusterName, commonconsts.EnvYataiApiToken)
		}
	}

	initContainers := make([]corev1.Container, 0)
	if strings.HasPrefix(modelDownloadURL, "hf://") {
		url := strings.TrimPrefix(modelDownloadURL, "hf://")
		arr := strings.Split(url, "@")
		if len(arr) != 3 {
			err = errors.Errorf("invalid Huggingface model download URL %s", modelDownloadURL)
			return
		}
		modelID := arr[0]
		modelRevsion := arr[1]
		modelEndpoint := arr[2]
		modelURL := path.Join(modelEndpoint, modelID, "resolve/main/config.json")

		var hfTokenValidatorCommandOutput bytes.Buffer
		err = template.Must(template.New("script").Parse(`
set -e

if curl -XHEAD -sIL -H "Authorization: Bearer $HF_TOKEN" "{{.ModelURL}}" | grep -iq 'x-error-code:.*GatedRepo'; then
    echo "Error: Model is gated. No access permission."
    exit 1
else
    echo "Model access granted."
    exit 0
fi
`)).Execute(&hfTokenValidatorCommandOutput, map[string]interface{}{
			"ModelURL": fmt.Sprintf("%s?revision=%s", modelURL, modelRevsion),
		})
		if err != nil {
			err = errors.Wrap(err, "failed to generate huggingface validation command")
			return
		}

		initContainers = append(initContainers, corev1.Container{
			Name:  "hf-token-validator",
			Image: internalImages.BentoDownloader,
			Command: []string{
				"bash",
				"-c",
				hfTokenValidatorCommandOutput.String(),
			},
			VolumeMounts: volumeMounts,
			Resources:    downloaderContainerResources,
			EnvFrom:      downloaderContainerEnvFrom,
		})
	}

	modelDirPath := "/juicefs-workspace"
	var modelSeedCommandOutput bytes.Buffer
	err = template.Must(template.New("script").Parse(`
set -e

mkdir -p {{.ModelDirPath}}
url="{{.ModelDownloadURL}}"

if [[ ${url} == hf://* ]]; then
	if [ -f "{{.ModelDirPath}}/{{.ModelVersion}}.exists" ]; then
		echo "Model {{.ModelDirPath}}/{{.ModelVersion}}.exists already exists, skip downloading"
		exit 0
	fi
else
	if [ -f "{{.ModelDirPath}}/.exists" ]; then
		echo "Model {{.ModelDirPath}} already exists, skip downloading"
		exit 0
	fi
fi

cleanup() {
	echo "Cleaning up..."
	rm -rf /tmp/model
	rm -f /tmp/downloaded.tar
}

trap cleanup EXIT

if [[ ${url} == hf://* ]]; then
	mkdir -p /tmp/model
	hf_url="${url:5}"
	model_id=$(echo "$hf_url" | awk -F '@' '{print $1}')
	revision=$(echo "$hf_url" | awk -F '@' '{print $2}')
	endpoint=$(echo "$hf_url" | awk -F '@' '{print $3}')
	export HF_ENDPOINT=${endpoint}

	echo "Downloading model ${model_id} (endpoint=${endpoint}, revision=${revision}) from Huggingface..."
	huggingface-cli download ${model_id} --revision ${revision} --cache-dir {{.ModelDirPath}}
else
	echo "Downloading model {{.ModelRepositoryName}}:{{.ModelVersion}} to /tmp/downloaded.tar..."
	if [[ ${url} == s3://* ]]; then
		echo "Downloading from s3..."
		aws s3 cp ${url} /tmp/downloaded.tar
	elif [[ ${url} == gs://* ]]; then
		echo "Downloading from GCS..."
		gsutil cp ${url} /tmp/downloaded.tar
	else
		curl --fail -L -H "{{.ModelDownloadHeader}}" ${url} --output /tmp/downloaded.tar --progress-bar
	fi
	cd {{.ModelDirPath}}
	echo "Extracting model tar file..."
	tar -xvf /tmp/downloaded.tar
fi

if [[ ${url} == hf://* ]]; then
	echo "Creating {{.ModelDirPath}}/{{.ModelVersion}}.exists file..."
	touch {{.ModelDirPath}}/{{.ModelVersion}}.exists
else
	echo "Creating {{.ModelDirPath}}/.exists file..."
	touch {{.ModelDirPath}}/.exists
fi

echo "Done"
`)).Execute(&modelSeedCommandOutput, map[string]interface{}{
		"ModelDirPath":        modelDirPath,
		"ModelDownloadURL":    modelDownloadURL,
		"ModelDownloadHeader": modelDownloadHeader,
		"ModelRepositoryName": modelRepositoryName,
		"ModelVersion":        modelVersion,
		"HuggingfaceModelDir": fmt.Sprintf("models--%s", strings.ReplaceAll(modelRepositoryName, "/", "--")),
	})
	if err != nil {
		err = errors.Wrap(err, "failed to generate download command")
		return
	}
	modelSeedCommand := modelSeedCommandOutput.String()
	pvcName := r.getModelPVCName(opt.BentoRequest, model)
	volumes = append(volumes, corev1.Volume{
		Name: pvcName,
		VolumeSource: corev1.VolumeSource{
			PersistentVolumeClaim: &corev1.PersistentVolumeClaimVolumeSource{
				ClaimName: pvcName,
			},
		},
	})
	containers = append(containers, corev1.Container{
		Name:  ModelSeederContainerName,
		Image: internalImages.BentoDownloader,
		Command: []string{
			"bash",
			"-c",
			modelSeedCommand,
		},
		VolumeMounts: append(volumeMounts, corev1.VolumeMount{
			Name:      pvcName,
			MountPath: modelDirPath,
		}),
		Resources: downloaderContainerResources,
		EnvFrom:   downloaderContainerEnvFrom,
		Env: []corev1.EnvVar{
			{
				Name:  "AWS_EC2_METADATA_DISABLED",
				Value: "true",
			},
		},
	})

	kubeAnnotations := make(map[string]string)
	kubeAnnotations[KubeAnnotationBentoRequestModelSeederHash] = opt.BentoRequest.Annotations[KubeAnnotationBentoRequestModelSeederHash]

	pod = &corev1.PodTemplateSpec{
		ObjectMeta: metav1.ObjectMeta{
			Labels:      kubeLabels,
			Annotations: kubeAnnotations,
		},
		Spec: corev1.PodSpec{
			RestartPolicy:  corev1.RestartPolicyNever,
			Volumes:        volumes,
			Containers:     containers,
			InitContainers: initContainers,
		},
	}

	var globalExtraPodSpec *resourcesv1alpha1.ExtraPodSpec

	configNamespace, err := commonconfig.GetYataiImageBuilderNamespace(ctx, func(ctx context.Context, namespace, name string) (*corev1.Secret, error) {
		secret := &corev1.Secret{}
		err := r.Get(ctx, types.NamespacedName{
			Namespace: namespace,
			Name:      name,
		}, secret)
		return secret, errors.Wrap(err, "get secret")
	})
	if err != nil {
		err = errors.Wrap(err, "failed to get Yatai image builder namespace")
		return
	}

	configCmName := "yatai-image-builder-config"
	r.Recorder.Eventf(opt.BentoRequest, corev1.EventTypeNormal, "GenerateModelSeederPod", "Getting configmap %s from namespace %s", configCmName, configNamespace)
	configCm := &corev1.ConfigMap{}
	err = r.Get(ctx, types.NamespacedName{Name: configCmName, Namespace: configNamespace}, configCm)
	configCmIsNotFound := k8serrors.IsNotFound(err)
	if err != nil && !configCmIsNotFound {
		err = errors.Wrap(err, "failed to get configmap")
		return
	}
	err = nil

	if !configCmIsNotFound {
		r.Recorder.Eventf(opt.BentoRequest, corev1.EventTypeNormal, "GenerateModelSeederPod", "Configmap %s is got from namespace %s", configCmName, configNamespace)

		globalExtraPodSpec = &resourcesv1alpha1.ExtraPodSpec{}

		if val, ok := configCm.Data["extra_pod_spec"]; ok {
			err = yaml.Unmarshal([]byte(val), globalExtraPodSpec)
			if err != nil {
				err = errors.Wrapf(err, "failed to yaml unmarshal extra_pod_spec, please check the configmap %s in namespace %s", configCmName, configNamespace)
				return
			}
		}
	} else {
		r.Recorder.Eventf(opt.BentoRequest, corev1.EventTypeNormal, "GenerateModelSeederPod", "Configmap %s is not found in namespace %s", configCmName, configNamespace)
	}

	if globalExtraPodSpec != nil {
		pod.Spec.PriorityClassName = globalExtraPodSpec.PriorityClassName
		pod.Spec.SchedulerName = globalExtraPodSpec.SchedulerName
		pod.Spec.NodeSelector = globalExtraPodSpec.NodeSelector
		pod.Spec.Affinity = globalExtraPodSpec.Affinity
		pod.Spec.Tolerations = globalExtraPodSpec.Tolerations
		pod.Spec.TopologySpreadConstraints = globalExtraPodSpec.TopologySpreadConstraints
		pod.Spec.ServiceAccountName = globalExtraPodSpec.ServiceAccountName
	}

	injectPodAffinity(&pod.Spec, opt.BentoRequest)

	return
}

type GenerateImageBuilderJobOption struct {
	ImageInfo    ImageInfo
	BentoRequest *resourcesv1alpha1.BentoRequest
}

func (r *BentoRequestReconciler) generateImageBuilderJob(ctx context.Context, opt GenerateImageBuilderJobOption) (job *batchv1.Job, err error) {
	// nolint: gosimple
	podTemplateSpec, err := r.generateImageBuilderPodTemplateSpec(ctx, GenerateImageBuilderPodTemplateSpecOption{
		ImageInfo:    opt.ImageInfo,
		BentoRequest: opt.BentoRequest,
	})
	if err != nil {
		err = errors.Wrap(err, "generate image builder pod template spec")
		return
	}
	kubeAnnotations := make(map[string]string)
	hashStr, err := r.getHashStr(opt.BentoRequest)
	if err != nil {
		err = errors.Wrap(err, "failed to get hash string")
		return
	}
	kubeAnnotations[KubeAnnotationBentoRequestHash] = hashStr
	job = &batchv1.Job{
		ObjectMeta: metav1.ObjectMeta{
			Name:        r.getImageBuilderJobName(),
			Namespace:   opt.BentoRequest.Namespace,
			Labels:      r.getImageBuilderJobLabels(opt.BentoRequest),
			Annotations: kubeAnnotations,
		},
		Spec: batchv1.JobSpec{
			Completions: pointer.Int32Ptr(1),
			Parallelism: pointer.Int32Ptr(1),
			PodFailurePolicy: &batchv1.PodFailurePolicy{
				Rules: []batchv1.PodFailurePolicyRule{
					{
						Action: batchv1.PodFailurePolicyActionFailJob,
						OnExitCodes: &batchv1.PodFailurePolicyOnExitCodesRequirement{
							ContainerName: pointer.StringPtr(BuilderContainerName),
							Operator:      batchv1.PodFailurePolicyOnExitCodesOpIn,
							Values:        []int32{BuilderJobFailedExitCode},
						},
					},
				},
			},
			Template: *podTemplateSpec,
		},
	}
	err = ctrl.SetControllerReference(opt.BentoRequest, job, r.Scheme)
	if err != nil {
		err = errors.Wrapf(err, "set controller reference for job %s", job.Name)
		return
	}
	return
}

func injectPodAffinity(podSpec *corev1.PodSpec, bentoRequest *resourcesv1alpha1.BentoRequest) {
	if podSpec.Affinity == nil {
		podSpec.Affinity = &corev1.Affinity{}
	}

	if podSpec.Affinity.PodAffinity == nil {
		podSpec.Affinity.PodAffinity = &corev1.PodAffinity{}
	}

	podSpec.Affinity.PodAffinity.PreferredDuringSchedulingIgnoredDuringExecution = append(podSpec.Affinity.PodAffinity.PreferredDuringSchedulingIgnoredDuringExecution, corev1.WeightedPodAffinityTerm{
		Weight: 100,
		PodAffinityTerm: corev1.PodAffinityTerm{
			LabelSelector: &metav1.LabelSelector{
				MatchLabels: map[string]string{
					commonconsts.KubeLabelBentoRequest: bentoRequest.Name,
				},
			},
			TopologyKey: corev1.LabelHostname,
		},
	})
}

const BuilderContainerName = "builder"
const BuilderJobFailedExitCode = 42
const ModelSeederContainerName = "seeder"
const ModelSeederJobFailedExitCode = 42

type GenerateImageBuilderPodTemplateSpecOption struct {
	ImageInfo    ImageInfo
	BentoRequest *resourcesv1alpha1.BentoRequest
}

func (r *BentoRequestReconciler) generateImageBuilderPodTemplateSpec(ctx context.Context, opt GenerateImageBuilderPodTemplateSpecOption) (pod *corev1.PodTemplateSpec, err error) {
	bentoRepositoryName, _, bentoVersion := xstrings.Partition(opt.BentoRequest.Spec.BentoTag, ":")
	kubeLabels := r.getImageBuilderPodLabels(opt.BentoRequest)

	inClusterImageName := opt.ImageInfo.InClusterImageName

	dockerConfigJSONSecretName := opt.ImageInfo.DockerConfigJSONSecretName

	dockerRegistryInsecure := opt.ImageInfo.DockerRegistryInsecure

	volumes := []corev1.Volume{
		{
			Name: "yatai",
			VolumeSource: corev1.VolumeSource{
				EmptyDir: &corev1.EmptyDirVolumeSource{},
			},
		},
		{
			Name: "workspace",
			VolumeSource: corev1.VolumeSource{
				EmptyDir: &corev1.EmptyDirVolumeSource{},
			},
		},
	}

	volumeMounts := []corev1.VolumeMount{
		{
			Name:      "yatai",
			MountPath: "/yatai",
		},
		{
			Name:      "workspace",
			MountPath: "/workspace",
		},
	}

	if dockerConfigJSONSecretName != "" {
		volumes = append(volumes, corev1.Volume{
			Name: dockerConfigJSONSecretName,
			VolumeSource: corev1.VolumeSource{
				Secret: &corev1.SecretVolumeSource{
					SecretName: dockerConfigJSONSecretName,
					Items: []corev1.KeyToPath{
						{
							Key:  ".dockerconfigjson",
							Path: "config.json",
						},
					},
				},
			},
		})
		volumeMounts = append(volumeMounts, corev1.VolumeMount{
			Name:      dockerConfigJSONSecretName,
			MountPath: "/kaniko/.docker/",
		})
	}

	var bento *schemasv1.BentoFullSchema
	yataiAPITokenSecretName := ""
	bentoDownloadURL := opt.BentoRequest.Spec.DownloadURL
	bentoDownloadHeader := ""

	if bentoDownloadURL == "" {
		var yataiClient_ **yataiclient.YataiClient
		var yataiConf_ **commonconfig.YataiConfig

		yataiClient_, yataiConf_, err = r.getYataiClient(ctx)
		if err != nil {
			err = errors.Wrap(err, "get yatai client")
			return
		}

		if yataiClient_ == nil || yataiConf_ == nil {
			err = errors.New("can't get yatai client, please check yatai configuration")
			return
		}

		yataiClient := *yataiClient_
		yataiConf := *yataiConf_

		r.Recorder.Eventf(opt.BentoRequest, corev1.EventTypeNormal, "GenerateImageBuilderPod", "Getting bento %s from yatai service", opt.BentoRequest.Spec.BentoTag)
		bento, err = yataiClient.GetBento(ctx, bentoRepositoryName, bentoVersion)
		if err != nil {
			err = errors.Wrap(err, "get bento")
			return
		}
		r.Recorder.Eventf(opt.BentoRequest, corev1.EventTypeNormal, "GenerateImageBuilderPod", "Got bento %s from yatai service", opt.BentoRequest.Spec.BentoTag)

		if bento.TransmissionStrategy != nil && *bento.TransmissionStrategy == modelschemas.TransmissionStrategyPresignedURL {
			var bento_ *schemasv1.BentoSchema
			r.Recorder.Eventf(opt.BentoRequest, corev1.EventTypeNormal, "GenerateImageBuilderPod", "Getting presigned url for bento %s from yatai service", opt.BentoRequest.Spec.BentoTag)
			bento_, err = yataiClient.PresignBentoDownloadURL(ctx, bentoRepositoryName, bentoVersion)
			if err != nil {
				err = errors.Wrap(err, "presign bento download url")
				return
			}
			r.Recorder.Eventf(opt.BentoRequest, corev1.EventTypeNormal, "GenerateImageBuilderPod", "Got presigned url for bento %s from yatai service", opt.BentoRequest.Spec.BentoTag)
			bentoDownloadURL = bento_.PresignedDownloadUrl
		} else {
			bentoDownloadURL = fmt.Sprintf("%s/api/v1/bento_repositories/%s/bentos/%s/download", yataiConf.Endpoint, bentoRepositoryName, bentoVersion)
			bentoDownloadHeader = fmt.Sprintf("%s: %s:%s:$%s", commonconsts.YataiApiTokenHeaderName, commonconsts.YataiImageBuilderComponentName, yataiConf.ClusterName, commonconsts.EnvYataiApiToken)
		}

		// nolint: gosec
		yataiAPITokenSecretName = "yatai-api-token"

		yataiAPITokenSecret := &corev1.Secret{
			ObjectMeta: metav1.ObjectMeta{
				Name:      yataiAPITokenSecretName,
				Namespace: opt.BentoRequest.Namespace,
			},
			StringData: map[string]string{
				commonconsts.EnvYataiApiToken: yataiConf.ApiToken,
			},
		}

		r.Recorder.Eventf(opt.BentoRequest, corev1.EventTypeNormal, "GenerateImageBuilderPod", "Getting secret %s in namespace %s", yataiAPITokenSecretName, opt.BentoRequest.Namespace)
		_yataiAPITokenSecret := &corev1.Secret{}
		err = r.Get(ctx, types.NamespacedName{Namespace: opt.BentoRequest.Namespace, Name: yataiAPITokenSecretName}, _yataiAPITokenSecret)
		isNotFound := k8serrors.IsNotFound(err)
		if err != nil && !isNotFound {
			err = errors.Wrapf(err, "failed to get secret %s", yataiAPITokenSecretName)
			return
		}

		if isNotFound {
			r.Recorder.Eventf(opt.BentoRequest, corev1.EventTypeNormal, "GenerateImageBuilderPod", "Secret %s is not found, so creating it in namespace %s", yataiAPITokenSecretName, opt.BentoRequest.Namespace)
			err = r.Create(ctx, yataiAPITokenSecret)
			isExists := k8serrors.IsAlreadyExists(err)
			if err != nil && !isExists {
				err = errors.Wrapf(err, "failed to create secret %s", yataiAPITokenSecretName)
				return
			}
			r.Recorder.Eventf(opt.BentoRequest, corev1.EventTypeNormal, "GenerateImageBuilderPod", "Secret %s is created in namespace %s", yataiAPITokenSecretName, opt.BentoRequest.Namespace)
		} else {
			r.Recorder.Eventf(opt.BentoRequest, corev1.EventTypeNormal, "GenerateImageBuilderPod", "Secret %s is found in namespace %s, so updating it", yataiAPITokenSecretName, opt.BentoRequest.Namespace)
			err = r.Update(ctx, yataiAPITokenSecret)
			if err != nil {
				err = errors.Wrapf(err, "failed to update secret %s", yataiAPITokenSecretName)
				return
			}
			r.Recorder.Eventf(opt.BentoRequest, corev1.EventTypeNormal, "GenerateImageBuilderPod", "Secret %s is updated in namespace %s", yataiAPITokenSecretName, opt.BentoRequest.Namespace)
		}
	}
	internalImages := commonconfig.GetInternalImages()
	logrus.Infof("Image builder is using the images %v", *internalImages)

	buildEngine := getBentoImageBuildEngine()

	privileged := buildEngine != BentoImageBuildEngineBuildkitRootless

	bentoDownloadCommandTemplate, err := template.New("downloadCommand").Parse(`
set -e

mkdir -p /workspace/buildcontext
url="{{.BentoDownloadURL}}"
echo "Downloading bento {{.BentoRepositoryName}}:{{.BentoVersion}} to /tmp/downloaded.tar..."
if [[ ${url} == s3://* ]]; then
	echo "Downloading from s3..."
	aws s3 cp ${url} /tmp/downloaded.tar
elif [[ ${url} == gs://* ]]; then
	echo "Downloading from GCS..."
	gsutil cp ${url} /tmp/downloaded.tar
else
	curl --fail -L -H "{{.BentoDownloadHeader}}" ${url} --output /tmp/downloaded.tar --progress-bar
fi
cd /workspace/buildcontext
echo "Extracting bento tar file..."
tar -xvf /tmp/downloaded.tar
echo "Removing bento tar file..."
rm /tmp/downloaded.tar
{{if not .Privileged}}
echo "Changing directory permission..."
chown -R 1000:1000 /workspace
{{end}}
echo "Done"
	`)

	if err != nil {
		err = errors.Wrap(err, "failed to parse download command template")
		return
	}

	var bentoDownloadCommandBuffer bytes.Buffer

	err = bentoDownloadCommandTemplate.Execute(&bentoDownloadCommandBuffer, map[string]interface{}{
		"BentoDownloadURL":    bentoDownloadURL,
		"BentoDownloadHeader": bentoDownloadHeader,
		"BentoRepositoryName": bentoRepositoryName,
		"BentoVersion":        bentoVersion,
		"Privileged":          privileged,
	})
	if err != nil {
		err = errors.Wrap(err, "failed to execute download command template")
		return
	}

	bentoDownloadCommand := bentoDownloadCommandBuffer.String()

	downloaderContainerResources := corev1.ResourceRequirements{
		Limits: corev1.ResourceList{
			corev1.ResourceCPU:    resource.MustParse("1000m"),
			corev1.ResourceMemory: resource.MustParse("3000Mi"),
		},
		Requests: corev1.ResourceList{
			corev1.ResourceCPU:    resource.MustParse("100m"),
			corev1.ResourceMemory: resource.MustParse("1000Mi"),
		},
	}

	downloaderContainerEnvFrom := opt.BentoRequest.Spec.DownloaderContainerEnvFrom

	if yataiAPITokenSecretName != "" {
		downloaderContainerEnvFrom = append(downloaderContainerEnvFrom, corev1.EnvFromSource{
			SecretRef: &corev1.SecretEnvSource{
				LocalObjectReference: corev1.LocalObjectReference{
					Name: yataiAPITokenSecretName,
				},
			},
		})
	}

	storeSchema := StoreSchemaAWS
	if strings.HasPrefix(bentoDownloadURL, "gs") {
		storeSchema = StoreSchemaGCP
	}
	var awsAccessKeySecretName, gcpAccessKeySecretName string
	if storeSchema == StoreSchemaAWS {
		// nolint: gosec
		awsAccessKeySecretName = opt.BentoRequest.Annotations[commonconsts.KubeAnnotationAWSAccessKeySecretName]
		if awsAccessKeySecretName == "" {
			awsAccessKeyID := os.Getenv(commonconsts.EnvAWSAccessKeyID)
			awsSecretAccessKey := os.Getenv(commonconsts.EnvAWSSecretAccessKey)
			if awsAccessKeyID != "" && awsSecretAccessKey != "" {
				// nolint: gosec
				awsAccessKeySecretName = YataiImageBuilderAWSAccessKeySecretName
				stringData := map[string]string{
					commonconsts.EnvAWSAccessKeyID:     awsAccessKeyID,
					commonconsts.EnvAWSSecretAccessKey: awsSecretAccessKey,
				}
				awsAccessKeySecret := &corev1.Secret{
					ObjectMeta: metav1.ObjectMeta{
						Name:      awsAccessKeySecretName,
						Namespace: opt.BentoRequest.Namespace,
					},
					StringData: stringData,
				}
				r.Recorder.Eventf(opt.BentoRequest, corev1.EventTypeNormal, "GenerateImageBuilderPod", "Creating or updating secret %s in namespace %s", awsAccessKeySecretName, opt.BentoRequest.Namespace)
				_, err = controllerutil.CreateOrUpdate(ctx, r.Client, awsAccessKeySecret, func() error {
					awsAccessKeySecret.StringData = stringData
					return nil
				})
				if err != nil {
					err = errors.Wrapf(err, "failed to create or update secret %s", awsAccessKeySecretName)
					return
				}
				r.Recorder.Eventf(opt.BentoRequest, corev1.EventTypeNormal, "GenerateImageBuilderPod", "Secret %s is created or updated in namespace %s", awsAccessKeySecretName, opt.BentoRequest.Namespace)
			}
		} else {
			downloaderContainerEnvFrom = append(downloaderContainerEnvFrom, corev1.EnvFromSource{
				SecretRef: &corev1.SecretEnvSource{
					LocalObjectReference: corev1.LocalObjectReference{
						Name: awsAccessKeySecretName,
					},
				},
			})
		}
	} else {
		// nolint: gosec
		gcpAccessKeySecretName = opt.BentoRequest.Annotations[commonconsts.KubeAnnotationGCPAccessKeySecretName]
		if gcpAccessKeySecretName == "" {
			gcpAccessKeyID := os.Getenv(commonconsts.EnvGCPAccessKeyID)
			gcpSecretAccessKey := os.Getenv(commonconsts.EnvGCPSecretAccessKey)
			if gcpAccessKeyID != "" && gcpSecretAccessKey != "" {
				// nolint: gosec
				gcpAccessKeySecretName = YataiImageBuilderGCPAccessKeySecretName
				stringData := map[string]string{
					commonconsts.EnvGCPAccessKeyID:     gcpAccessKeyID,
					commonconsts.EnvGCPSecretAccessKey: gcpSecretAccessKey,
				}
				gcpAccessKeySecret := &corev1.Secret{
					ObjectMeta: metav1.ObjectMeta{
						Name:      gcpAccessKeySecretName,
						Namespace: opt.BentoRequest.Namespace,
					},
					StringData: stringData,
				}
				r.Recorder.Eventf(opt.BentoRequest, corev1.EventTypeNormal, "GenerateImageBuilderPod", "Creating or updating secret %s in namespace %s", gcpAccessKeySecretName, opt.BentoRequest.Namespace)
				_, err = controllerutil.CreateOrUpdate(ctx, r.Client, gcpAccessKeySecret, func() error {
					gcpAccessKeySecret.StringData = stringData
					return nil
				})
				if err != nil {
					err = errors.Wrapf(err, "failed to create or update secret %s", gcpAccessKeySecretName)
					return
				}
				r.Recorder.Eventf(opt.BentoRequest, corev1.EventTypeNormal, "GenerateImageBuilderPod", "Secret %s is created or updated in namespace %s", gcpAccessKeySecretName, opt.BentoRequest.Namespace)
			}
		} else {
			downloaderContainerEnvFrom = append(downloaderContainerEnvFrom, corev1.EnvFromSource{
				SecretRef: &corev1.SecretEnvSource{
					LocalObjectReference: corev1.LocalObjectReference{
						Name: gcpAccessKeySecretName,
					},
				},
			})
		}
	}

	initContainers := []corev1.Container{
		{
			Name:  "bento-downloader",
			Image: internalImages.BentoDownloader,
			Command: []string{
				"bash",
				"-c",
				bentoDownloadCommand,
			},
			VolumeMounts: volumeMounts,
			Resources:    downloaderContainerResources,
			EnvFrom:      downloaderContainerEnvFrom,
			Env: []corev1.EnvVar{
				{
					Name:  "AWS_EC2_METADATA_DISABLED",
					Value: "true",
				},
			},
		},
	}

	containers := make([]corev1.Container, 0)

	separateModels := isSeparateModels(opt.BentoRequest)

	models := opt.BentoRequest.Spec.Models
	modelsSeen := map[string]struct{}{}
	for _, model := range models {
		modelsSeen[model.Tag] = struct{}{}
	}

	if bento != nil {
		for _, modelTag := range bento.Manifest.Models {
			if _, ok := modelsSeen[modelTag]; !ok {
				models = append(models, resourcesv1alpha1.BentoModel{
					Tag: modelTag,
				})
			}
		}
	}

	for idx, model := range models {
		if separateModels {
			continue
		}
		modelRepositoryName, _, modelVersion := xstrings.Partition(model.Tag, ":")
		modelDownloadURL := model.DownloadURL
		modelDownloadHeader := ""
		if modelDownloadURL == "" {
			if bento == nil {
				continue
			}

			var yataiClient_ **yataiclient.YataiClient
			var yataiConf_ **commonconfig.YataiConfig

			yataiClient_, yataiConf_, err = r.getYataiClient(ctx)
			if err != nil {
				err = errors.Wrap(err, "get yatai client")
				return
			}

			if yataiClient_ == nil || yataiConf_ == nil {
				err = errors.New("can't get yatai client, please check yatai configuration")
				return
			}

			yataiClient := *yataiClient_
			yataiConf := *yataiConf_

			var model_ *schemasv1.ModelFullSchema
			r.Recorder.Eventf(opt.BentoRequest, corev1.EventTypeNormal, "GenerateImageBuilderPod", "Getting model %s from yatai service", model.Tag)
			model_, err = yataiClient.GetModel(ctx, modelRepositoryName, modelVersion)
			if err != nil {
				err = errors.Wrap(err, "get model")
				return
			}
			r.Recorder.Eventf(opt.BentoRequest, corev1.EventTypeNormal, "GenerateImageBuilderPod", "Model %s is got from yatai service", model.Tag)

			if model_.TransmissionStrategy != nil && *model_.TransmissionStrategy == modelschemas.TransmissionStrategyPresignedURL {
				var model0 *schemasv1.ModelSchema
				r.Recorder.Eventf(opt.BentoRequest, corev1.EventTypeNormal, "GenerateImageBuilderPod", "Getting presigned url for model %s from yatai service", model.Tag)
				model0, err = yataiClient.PresignModelDownloadURL(ctx, modelRepositoryName, modelVersion)
				if err != nil {
					err = errors.Wrap(err, "presign model download url")
					return
				}
				r.Recorder.Eventf(opt.BentoRequest, corev1.EventTypeNormal, "GenerateImageBuilderPod", "Presigned url for model %s is got from yatai service", model.Tag)
				modelDownloadURL = model0.PresignedDownloadUrl
			} else {
				modelDownloadURL = fmt.Sprintf("%s/api/v1/model_repositories/%s/models/%s/download", yataiConf.Endpoint, modelRepositoryName, modelVersion)
				modelDownloadHeader = fmt.Sprintf("%s: %s:%s:$%s", commonconsts.YataiApiTokenHeaderName, commonconsts.YataiImageBuilderComponentName, yataiConf.ClusterName, commonconsts.EnvYataiApiToken)
			}
		}
		modelRepositoryDirPath := fmt.Sprintf("/workspace/buildcontext/models/%s", modelRepositoryName)
		modelDirPath := filepath.Join(modelRepositoryDirPath, modelVersion)
		var modelDownloadCommandOutput bytes.Buffer
		err = template.Must(template.New("script").Parse(`
set -e

mkdir -p {{.ModelDirPath}}
url="{{.ModelDownloadURL}}"

if [[ ${url} == hf://* ]]; then
	mkdir -p /tmp/model
	hf_url="${url:5}"
	model_id=$(echo "$hf_url" | awk -F '@' '{print $1}')
	revision=$(echo "$hf_url" | awk -F '@' '{print $2}')
	endpoint=$(echo "$hf_url" | awk -F '@' '{print $3}')
	export HF_ENDPOINT=${endpoint}

	echo "Downloading model ${model_id} (endpoint=${endpoint}, revision=${revision}) from Huggingface..."
	huggingface-cli download ${model_id} --revision ${revision} --cache-dir {{.ModelDirPath}}
else
	echo "Downloading model {{.ModelRepositoryName}}:{{.ModelVersion}} to /tmp/downloaded.tar..."
	if [[ ${url} == s3://* ]]; then
		echo "Downloading from s3..."
		aws s3 cp ${url} /tmp/downloaded.tar
	elif [[ ${url} == gs://* ]]; then
		echo "Downloading from GCS..."
		gsutil cp ${url} /tmp/downloaded.tar
	else
		curl --fail -L -H "{{.ModelDownloadHeader}}" ${url} --output /tmp/downloaded.tar --progress-bar
	fi
	cd {{.ModelDirPath}}
	echo "Extracting model tar file..."
	tar -xvf /tmp/downloaded.tar
	echo -n '{{.ModelVersion}}' > {{.ModelRepositoryDirPath}}/latest
	echo "Removing model tar file..."
	rm /tmp/downloaded.tar
	{{if not .Privileged}}
	echo "Changing directory permission..."
	chown -R 1000:1000 /workspace
	{{end}}
fi
echo "Done"
`)).Execute(&modelDownloadCommandOutput, map[string]interface{}{
			"ModelDirPath":           modelDirPath,
			"ModelDownloadURL":       modelDownloadURL,
			"ModelDownloadHeader":    modelDownloadHeader,
			"ModelRepositoryDirPath": modelRepositoryDirPath,
			"ModelRepositoryName":    modelRepositoryName,
			"ModelVersion":           modelVersion,
			"Privileged":             privileged,
			"HuggingfaceModelDir":    fmt.Sprintf("models--%s", strings.ReplaceAll(modelRepositoryName, "/", "--")),
		})
		if err != nil {
			err = errors.Wrap(err, "failed to generate download command")
			return
		}
		modelDownloadCommand := modelDownloadCommandOutput.String()
		initContainers = append(initContainers, corev1.Container{
			Name:  fmt.Sprintf("model-downloader-%d", idx),
			Image: internalImages.BentoDownloader,
			Command: []string{
				"bash",
				"-c",
				modelDownloadCommand,
			},
			VolumeMounts: volumeMounts,
			Resources:    downloaderContainerResources,
			EnvFrom:      downloaderContainerEnvFrom,
			Env: []corev1.EnvVar{
				{
					Name:  "AWS_EC2_METADATA_DISABLED",
					Value: "true",
				},
			},
		})
	}

	var globalExtraPodMetadata *resourcesv1alpha1.ExtraPodMetadata
	var globalExtraPodSpec *resourcesv1alpha1.ExtraPodSpec
	var globalExtraContainerEnv []corev1.EnvVar
	var globalDefaultImageBuilderContainerResources *corev1.ResourceRequirements
	var buildArgs []string
	var builderArgs []string

	configNamespace, err := commonconfig.GetYataiImageBuilderNamespace(ctx, func(ctx context.Context, namespace, name string) (*corev1.Secret, error) {
		secret := &corev1.Secret{}
		err := r.Get(ctx, types.NamespacedName{
			Namespace: namespace,
			Name:      name,
		}, secret)
		return secret, errors.Wrap(err, "get secret")
	})
	if err != nil {
		err = errors.Wrap(err, "failed to get Yatai image builder namespace")
		return
	}

	configCmName := "yatai-image-builder-config"
	r.Recorder.Eventf(opt.BentoRequest, corev1.EventTypeNormal, "GenerateImageBuilderPod", "Getting configmap %s from namespace %s", configCmName, configNamespace)
	configCm := &corev1.ConfigMap{}
	err = r.Get(ctx, types.NamespacedName{Name: configCmName, Namespace: configNamespace}, configCm)
	configCmIsNotFound := k8serrors.IsNotFound(err)
	if err != nil && !configCmIsNotFound {
		err = errors.Wrap(err, "failed to get configmap")
		return
	}
	err = nil // nolint: ineffassign

	if !configCmIsNotFound {
		r.Recorder.Eventf(opt.BentoRequest, corev1.EventTypeNormal, "GenerateImageBuilderPod", "Configmap %s is got from namespace %s", configCmName, configNamespace)

		globalExtraPodMetadata = &resourcesv1alpha1.ExtraPodMetadata{}

		if val, ok := configCm.Data["extra_pod_metadata"]; ok {
			err = yaml.Unmarshal([]byte(val), globalExtraPodMetadata)
			if err != nil {
				err = errors.Wrapf(err, "failed to yaml unmarshal extra_pod_metadata, please check the configmap %s in namespace %s", configCmName, configNamespace)
				return
			}
		}

		globalExtraPodSpec = &resourcesv1alpha1.ExtraPodSpec{}

		if val, ok := configCm.Data["extra_pod_spec"]; ok {
			err = yaml.Unmarshal([]byte(val), globalExtraPodSpec)
			if err != nil {
				err = errors.Wrapf(err, "failed to yaml unmarshal extra_pod_spec, please check the configmap %s in namespace %s", configCmName, configNamespace)
				return
			}
		}

		globalExtraContainerEnv = []corev1.EnvVar{}

		if val, ok := configCm.Data["extra_container_env"]; ok {
			err = yaml.Unmarshal([]byte(val), &globalExtraContainerEnv)
			if err != nil {
				err = errors.Wrapf(err, "failed to yaml unmarshal extra_container_env, please check the configmap %s in namespace %s", configCmName, configNamespace)
				return
			}
		}

		if val, ok := configCm.Data["default_image_builder_container_resources"]; ok {
			globalDefaultImageBuilderContainerResources = &corev1.ResourceRequirements{}
			err = yaml.Unmarshal([]byte(val), globalDefaultImageBuilderContainerResources)
			if err != nil {
				err = errors.Wrapf(err, "failed to yaml unmarshal default_image_builder_container_resources, please check the configmap %s in namespace %s", configCmName, configNamespace)
				return
			}
		}

		buildArgs = []string{}

		if val, ok := configCm.Data["build_args"]; ok {
			err = yaml.Unmarshal([]byte(val), &buildArgs)
			if err != nil {
				err = errors.Wrapf(err, "failed to yaml unmarshal build_args, please check the configmap %s in namespace %s", configCmName, configNamespace)
				return
			}
		}

		builderArgs = []string{}
		if val, ok := configCm.Data["builder_args"]; ok {
			err = yaml.Unmarshal([]byte(val), &builderArgs)
			if err != nil {
				err = errors.Wrapf(err, "failed to yaml unmarshal builder_args, please check the configmap %s in namespace %s", configCmName, configNamespace)
				return
			}
		}
		logrus.Info("passed in builder args: ", builderArgs)
	} else {
		r.Recorder.Eventf(opt.BentoRequest, corev1.EventTypeNormal, "GenerateImageBuilderPod", "Configmap %s is not found in namespace %s", configCmName, configNamespace)
	}

	if buildArgs == nil {
		buildArgs = make([]string, 0)
	}

	if opt.BentoRequest.Spec.BuildArgs != nil {
		buildArgs = append(buildArgs, opt.BentoRequest.Spec.BuildArgs...)
	}

	dockerFilePath := "/workspace/buildcontext/env/docker/Dockerfile"

	builderContainerEnvFrom := make([]corev1.EnvFromSource, 0)
	builderContainerEnvs := []corev1.EnvVar{
		{
			Name:  "DOCKER_CONFIG",
			Value: "/kaniko/.docker/",
		},
		{
			Name:  "IFS",
			Value: "''",
		},
	}

	if UsingAWSECRWithIAMRole() {
		builderContainerEnvs = append(builderContainerEnvs, corev1.EnvVar{
			Name:  "AWS_REGION",
			Value: GetAWSECRRegion(),
		}, corev1.EnvVar{
			Name:  "AWS_SDK_LOAD_CONFIG",
			Value: "true",
		})
	}

	kanikoCacheRepo := os.Getenv("KANIKO_CACHE_REPO")
	if kanikoCacheRepo == "" {
		kanikoCacheRepo = opt.ImageInfo.DockerRegistry.BentosRepositoryURIInCluster
	}

	kubeAnnotations := make(map[string]string)
	kubeAnnotations[KubeAnnotationBentoRequestImageBuiderHash] = opt.BentoRequest.Annotations[KubeAnnotationBentoRequestImageBuiderHash]

	command := []string{
		"/kaniko/executor",
	}
	args := []string{
		"--context=/workspace/buildcontext",
		"--verbosity=info",
		"--image-fs-extract-retry=3",
		"--cache=true",
		fmt.Sprintf("--cache-repo=%s", kanikoCacheRepo),
		"--compressed-caching=false",
		"--compression=zstd",
		"--compression-level=-7",
		fmt.Sprintf("--dockerfile=%s", dockerFilePath),
		fmt.Sprintf("--insecure=%v", dockerRegistryInsecure),
		fmt.Sprintf("--destination=%s", inClusterImageName),
	}

	kanikoSnapshotMode := os.Getenv("KANIKO_SNAPSHOT_MODE")
	if kanikoSnapshotMode != "" {
		args = append(args, fmt.Sprintf("--snapshot-mode=%s", kanikoSnapshotMode))
	}

	var builderImage string
	switch buildEngine {
	case BentoImageBuildEngineKaniko:
		builderImage = internalImages.Kaniko
		if isEstargzEnabled() {
			builderContainerEnvs = append(builderContainerEnvs, corev1.EnvVar{
				Name:  "GGCR_EXPERIMENT_ESTARGZ",
				Value: "1",
			})
		}
	case BentoImageBuildEngineBuildkit:
		builderImage = internalImages.Buildkit
	case BentoImageBuildEngineBuildkitRootless:
		builderImage = internalImages.BuildkitRootless
	default:
		err = errors.Errorf("unknown bento image build engine %s", buildEngine)
		return
	}

	isBuildkit := buildEngine == BentoImageBuildEngineBuildkit || buildEngine == BentoImageBuildEngineBuildkitRootless

	if isBuildkit {
		output := fmt.Sprintf("type=image,name=%s,push=true,registry.insecure=%v", inClusterImageName, dockerRegistryInsecure)
		buildkitdFlags := []string{}
		if !privileged {
			buildkitdFlags = append(buildkitdFlags, "--oci-worker-no-process-sandbox")
		}
		if isEstargzEnabled() {
			buildkitdFlags = append(buildkitdFlags, "--oci-worker-snapshotter=stargz")
			output += ",oci-mediatypes=true,compression=estargz,force-compression=true"
		}
		if len(buildkitdFlags) > 0 {
			builderContainerEnvs = append(builderContainerEnvs, corev1.EnvVar{
				Name:  "BUILDKITD_FLAGS",
				Value: strings.Join(buildkitdFlags, " "),
			})
		}
		command = []string{"buildctl-daemonless.sh"}
		args = []string{
			"build",
			"--frontend",
			"dockerfile.v0",
			"--local",
			"context=/workspace/buildcontext",
			"--local",
			fmt.Sprintf("dockerfile=%s", filepath.Dir(dockerFilePath)),
			"--output",
			output,
		}
		buildkitS3CacheEnabled := os.Getenv("BUILDKIT_S3_CACHE_ENABLED") == commonconsts.KubeLabelValueTrue
		if buildkitS3CacheEnabled {
			buildkitS3CacheRegion := os.Getenv("BUILDKIT_S3_CACHE_REGION")
			buildkitS3CacheBucket := os.Getenv("BUILDKIT_S3_CACHE_BUCKET")
			cachedImageName := fmt.Sprintf("%s%s", getBentoImagePrefix(opt.BentoRequest), bentoRepositoryName)
			args = append(args, "--import-cache", fmt.Sprintf("type=s3,region=%s,bucket=%s,name=%s", buildkitS3CacheRegion, buildkitS3CacheBucket, cachedImageName))
			args = append(args, "--export-cache", fmt.Sprintf("type=s3,region=%s,bucket=%s,name=%s,mode=max,compression=zstd,ignore-error=true", buildkitS3CacheRegion, buildkitS3CacheBucket, cachedImageName))
			if storeSchema == StoreSchemaAWS && awsAccessKeySecretName != "" {
				builderContainerEnvFrom = append(builderContainerEnvFrom, corev1.EnvFromSource{
					SecretRef: &corev1.SecretEnvSource{
						LocalObjectReference: corev1.LocalObjectReference{
							Name: awsAccessKeySecretName,
						},
					},
				})
			} else if gcpAccessKeySecretName != "" {
				builderContainerEnvFrom = append(builderContainerEnvFrom, corev1.EnvFromSource{
					SecretRef: &corev1.SecretEnvSource{
						LocalObjectReference: corev1.LocalObjectReference{
							Name: gcpAccessKeySecretName,
						},
					},
				})
			}
		} else {
			cacheRepo := os.Getenv("BUILDKIT_CACHE_REPO")
			if cacheRepo == "" {
				cacheRepo = opt.ImageInfo.DockerRegistry.BentosRepositoryURIInCluster
			}
			args = append(args, "--export-cache", fmt.Sprintf("type=registry,ref=%s:buildcache,mode=max,compression=zstd,ignore-error=true", cacheRepo))
			args = append(args, "--import-cache", fmt.Sprintf("type=registry,ref=%s:buildcache", cacheRepo))
		}
	}

	var builderContainerSecurityContext *corev1.SecurityContext

	if buildEngine == BentoImageBuildEngineBuildkit {
		builderContainerSecurityContext = &corev1.SecurityContext{
			Privileged: pointer.BoolPtr(true),
		}
	} else if buildEngine == BentoImageBuildEngineBuildkitRootless {
		kubeAnnotations["container.apparmor.security.beta.kubernetes.io/builder"] = "unconfined"
		builderContainerSecurityContext = &corev1.SecurityContext{
			SeccompProfile: &corev1.SeccompProfile{
				Type: corev1.SeccompProfileTypeUnconfined,
			},
			RunAsUser:  pointer.Int64Ptr(1000),
			RunAsGroup: pointer.Int64Ptr(1000),
		}
	}

	// add build args to pass via --build-arg
	for _, buildArg := range buildArgs {
		quotedBuildArg := unix.SingleQuote.Quote(buildArg)
		if isBuildkit {
			args = append(args, "--opt", fmt.Sprintf("build-arg:%s", quotedBuildArg))
		} else {
			args = append(args, fmt.Sprintf("--build-arg=%s", quotedBuildArg))
		}
	}
	// add other arguments to builder
	args = append(args, builderArgs...)
	logrus.Info("yatai-image-builder args: ", args)

	// nolint: gosec
	buildArgsSecretName := "yatai-image-builder-build-args"
	r.Recorder.Eventf(opt.BentoRequest, corev1.EventTypeNormal, "GenerateImageBuilderPod", "Getting secret %s from namespace %s", buildArgsSecretName, configNamespace)
	buildArgsSecret := &corev1.Secret{}
	err = r.Get(ctx, types.NamespacedName{Name: buildArgsSecretName, Namespace: configNamespace}, buildArgsSecret)
	buildArgsSecretIsNotFound := k8serrors.IsNotFound(err)
	if err != nil && !buildArgsSecretIsNotFound {
		err = errors.Wrap(err, "failed to get secret")
		return
	}

	if !buildArgsSecretIsNotFound {
		r.Recorder.Eventf(opt.BentoRequest, corev1.EventTypeNormal, "GenerateImageBuilderPod", "Secret %s is got from namespace %s", buildArgsSecretName, configNamespace)
		if configNamespace != opt.BentoRequest.Namespace {
			r.Recorder.Eventf(opt.BentoRequest, corev1.EventTypeNormal, "GenerateImageBuilderPod", "Secret %s is in namespace %s, but BentoRequest is in namespace %s, so we need to copy the secret to BentoRequest namespace", buildArgsSecretName, configNamespace, opt.BentoRequest.Namespace)
			r.Recorder.Eventf(opt.BentoRequest, corev1.EventTypeNormal, "GenerateImageBuilderPod", "Getting secret %s in namespace %s", buildArgsSecretName, opt.BentoRequest.Namespace)
			_buildArgsSecret := &corev1.Secret{}
			err = r.Get(ctx, types.NamespacedName{Namespace: opt.BentoRequest.Namespace, Name: buildArgsSecretName}, _buildArgsSecret)
			localBuildArgsSecretIsNotFound := k8serrors.IsNotFound(err)
			if err != nil && !localBuildArgsSecretIsNotFound {
				err = errors.Wrapf(err, "failed to get secret %s from namespace %s", buildArgsSecretName, opt.BentoRequest.Namespace)
				return
			}
			if localBuildArgsSecretIsNotFound {
				r.Recorder.Eventf(opt.BentoRequest, corev1.EventTypeNormal, "GenerateImageBuilderPod", "Copying secret %s from namespace %s to namespace %s", buildArgsSecretName, configNamespace, opt.BentoRequest.Namespace)
				err = r.Create(ctx, &corev1.Secret{
					ObjectMeta: metav1.ObjectMeta{
						Name:      buildArgsSecretName,
						Namespace: opt.BentoRequest.Namespace,
					},
					Data: buildArgsSecret.Data,
				})
				if err != nil {
					err = errors.Wrapf(err, "failed to create secret %s in namespace %s", buildArgsSecretName, opt.BentoRequest.Namespace)
					return
				}
			} else {
				r.Recorder.Eventf(opt.BentoRequest, corev1.EventTypeNormal, "GenerateImageBuilderPod", "Secret %s is already in namespace %s", buildArgsSecretName, opt.BentoRequest.Namespace)
				r.Recorder.Eventf(opt.BentoRequest, corev1.EventTypeNormal, "GenerateImageBuilderPod", "Updating secret %s in namespace %s", buildArgsSecretName, opt.BentoRequest.Namespace)
				err = r.Update(ctx, &corev1.Secret{
					ObjectMeta: metav1.ObjectMeta{
						Name:      buildArgsSecretName,
						Namespace: opt.BentoRequest.Namespace,
					},
					Data: buildArgsSecret.Data,
				})
				if err != nil {
					err = errors.Wrapf(err, "failed to update secret %s in namespace %s", buildArgsSecretName, opt.BentoRequest.Namespace)
					return
				}
			}
		}

		for key := range buildArgsSecret.Data {
			envName := fmt.Sprintf("BENTOML_BUILD_ARG_%s", strings.ReplaceAll(strings.ToUpper(strcase.ToKebab(key)), "-", "_"))
			builderContainerEnvs = append(builderContainerEnvs, corev1.EnvVar{
				Name: envName,
				ValueFrom: &corev1.EnvVarSource{
					SecretKeyRef: &corev1.SecretKeySelector{
						LocalObjectReference: corev1.LocalObjectReference{
							Name: buildArgsSecretName,
						},
						Key: key,
					},
				},
			})

			if isBuildkit {
				args = append(args, "--opt", fmt.Sprintf("build-arg:%s=$(%s)", key, envName))
			} else {
				args = append(args, fmt.Sprintf("--build-arg=%s=$(%s)", key, envName))
			}
		}
	} else {
		r.Recorder.Eventf(opt.BentoRequest, corev1.EventTypeNormal, "GenerateImageBuilderPod", "Secret %s is not found in namespace %s", buildArgsSecretName, configNamespace)
	}

	builderContainerArgs := []string{
		"-c",
		fmt.Sprintf("%s && exit 0 || exit %d", shquot.POSIXShell(append(command, args...)), BuilderJobFailedExitCode),
	}

	container := corev1.Container{
		Name:            BuilderContainerName,
		Image:           builderImage,
		ImagePullPolicy: corev1.PullAlways,
		Command:         []string{"sh"},
		Args:            builderContainerArgs,
		VolumeMounts:    volumeMounts,
		Env:             builderContainerEnvs,
		EnvFrom:         builderContainerEnvFrom,
		TTY:             true,
		Stdin:           true,
		SecurityContext: builderContainerSecurityContext,
	}

	if globalDefaultImageBuilderContainerResources != nil {
		container.Resources = *globalDefaultImageBuilderContainerResources
	}

	if opt.BentoRequest.Spec.ImageBuilderContainerResources != nil {
		container.Resources = *opt.BentoRequest.Spec.ImageBuilderContainerResources
	}

	containers = append(containers, container)

	pod = &corev1.PodTemplateSpec{
		ObjectMeta: metav1.ObjectMeta{
			Labels:      kubeLabels,
			Annotations: kubeAnnotations,
		},
		Spec: corev1.PodSpec{
			RestartPolicy:  corev1.RestartPolicyNever,
			Volumes:        volumes,
			InitContainers: initContainers,
			Containers:     containers,
		},
	}

	if globalExtraPodMetadata != nil {
		for k, v := range globalExtraPodMetadata.Annotations {
			pod.Annotations[k] = v
		}

		for k, v := range globalExtraPodMetadata.Labels {
			pod.Labels[k] = v
		}
	}

	if opt.BentoRequest.Spec.ImageBuilderExtraPodMetadata != nil {
		for k, v := range opt.BentoRequest.Spec.ImageBuilderExtraPodMetadata.Annotations {
			pod.Annotations[k] = v
		}

		for k, v := range opt.BentoRequest.Spec.ImageBuilderExtraPodMetadata.Labels {
			pod.Labels[k] = v
		}
	}

	if globalExtraPodSpec != nil {
		pod.Spec.PriorityClassName = globalExtraPodSpec.PriorityClassName
		pod.Spec.SchedulerName = globalExtraPodSpec.SchedulerName
		pod.Spec.NodeSelector = globalExtraPodSpec.NodeSelector
		pod.Spec.Affinity = globalExtraPodSpec.Affinity
		pod.Spec.Tolerations = globalExtraPodSpec.Tolerations
		pod.Spec.TopologySpreadConstraints = globalExtraPodSpec.TopologySpreadConstraints
		pod.Spec.ServiceAccountName = globalExtraPodSpec.ServiceAccountName
	}

	if opt.BentoRequest.Spec.ImageBuilderExtraPodSpec != nil {
		if opt.BentoRequest.Spec.ImageBuilderExtraPodSpec.PriorityClassName != "" {
			pod.Spec.PriorityClassName = opt.BentoRequest.Spec.ImageBuilderExtraPodSpec.PriorityClassName
		}

		if opt.BentoRequest.Spec.ImageBuilderExtraPodSpec.SchedulerName != "" {
			pod.Spec.SchedulerName = opt.BentoRequest.Spec.ImageBuilderExtraPodSpec.SchedulerName
		}

		if opt.BentoRequest.Spec.ImageBuilderExtraPodSpec.NodeSelector != nil {
			pod.Spec.NodeSelector = opt.BentoRequest.Spec.ImageBuilderExtraPodSpec.NodeSelector
		}

		if opt.BentoRequest.Spec.ImageBuilderExtraPodSpec.Affinity != nil {
			pod.Spec.Affinity = opt.BentoRequest.Spec.ImageBuilderExtraPodSpec.Affinity
		}

		if opt.BentoRequest.Spec.ImageBuilderExtraPodSpec.Tolerations != nil {
			pod.Spec.Tolerations = opt.BentoRequest.Spec.ImageBuilderExtraPodSpec.Tolerations
		}

		if opt.BentoRequest.Spec.ImageBuilderExtraPodSpec.TopologySpreadConstraints != nil {
			pod.Spec.TopologySpreadConstraints = opt.BentoRequest.Spec.ImageBuilderExtraPodSpec.TopologySpreadConstraints
		}

		if opt.BentoRequest.Spec.ImageBuilderExtraPodSpec.ServiceAccountName != "" {
			pod.Spec.ServiceAccountName = opt.BentoRequest.Spec.ImageBuilderExtraPodSpec.ServiceAccountName
		}
	}

	injectPodAffinity(&pod.Spec, opt.BentoRequest)

	if pod.Spec.ServiceAccountName == "" {
		serviceAccounts := &corev1.ServiceAccountList{}
		err = r.List(ctx, serviceAccounts, client.InNamespace(opt.BentoRequest.Namespace), client.MatchingLabels{
			commonconsts.KubeLabelYataiImageBuilderPod: commonconsts.KubeLabelValueTrue,
		})
		if err != nil {
			err = errors.Wrapf(err, "failed to list service accounts in namespace %s", opt.BentoRequest.Namespace)
			return
		}
		if len(serviceAccounts.Items) > 0 {
			pod.Spec.ServiceAccountName = serviceAccounts.Items[0].Name
		} else {
			pod.Spec.ServiceAccountName = "default"
		}
	}

	for i, c := range pod.Spec.InitContainers {
		env := c.Env
		if globalExtraContainerEnv != nil {
			env = append(env, globalExtraContainerEnv...)
		}
		env = append(env, opt.BentoRequest.Spec.ImageBuilderExtraContainerEnv...)
		pod.Spec.InitContainers[i].Env = env
	}
	for i, c := range pod.Spec.Containers {
		env := c.Env
		if globalExtraContainerEnv != nil {
			env = append(env, globalExtraContainerEnv...)
		}
		env = append(env, opt.BentoRequest.Spec.ImageBuilderExtraContainerEnv...)
		pod.Spec.Containers[i].Env = env
	}

	return
}

func (r *BentoRequestReconciler) getHashStr(bentoRequest *resourcesv1alpha1.BentoRequest) (string, error) {
	var hash uint64
	hash, err := hashstructure.Hash(struct {
		Spec        resourcesv1alpha1.BentoRequestSpec
		Labels      map[string]string
		Annotations map[string]string
	}{
		Spec:        bentoRequest.Spec,
		Labels:      bentoRequest.Labels,
		Annotations: bentoRequest.Annotations,
	}, hashstructure.FormatV2, nil)
	if err != nil {
		err = errors.Wrap(err, "get bentoRequest CR spec hash")
		return "", err
	}
	hashStr := strconv.FormatUint(hash, 10)
	return hashStr, nil
}

func (r *BentoRequestReconciler) doRegisterYataiComponent() (err error) {
	logs := log.Log.WithValues("func", "doRegisterYataiComponent")

	ctx, cancel := context.WithTimeout(context.TODO(), time.Minute*5)
	defer cancel()

	logs.Info("getting yatai client")
	yataiClient, yataiConf, err := r.getYataiClient(ctx)
	if err != nil {
		err = errors.Wrap(err, "get yatai client")
		return
	}

	if yataiClient == nil || yataiConf == nil {
		logs.Info("can't get yatai client, skip registering")
		return
	}

	yataiClient_ := *yataiClient
	yataiConf_ := *yataiConf

	namespace, err := commonconfig.GetYataiImageBuilderNamespace(ctx, func(ctx context.Context, namespace, name string) (*corev1.Secret, error) {
		secret := &corev1.Secret{}
		err := r.Get(ctx, types.NamespacedName{
			Namespace: namespace,
			Name:      name,
		}, secret)
		return secret, errors.Wrap(err, "get secret")
	})
	if err != nil {
		err = errors.Wrap(err, "get yatai image builder namespace")
		return
	}

	_, err = yataiClient_.RegisterYataiComponent(ctx, yataiConf_.ClusterName, &schemasv1.RegisterYataiComponentSchema{
		Name:          modelschemas.YataiComponentNameImageBuilder,
		KubeNamespace: namespace,
		Version:       version.Version,
		SelectorLabels: map[string]string{
			"app.kubernetes.io/name": "yatai-image-builder",
		},
		Manifest: &modelschemas.YataiComponentManifestSchema{
			SelectorLabels: map[string]string{
				"app.kubernetes.io/name": "yatai-image-builder",
			},
			LatestCRDVersion: "v1alpha1",
		},
	})

	err = errors.Wrap(err, "register yatai component")
	return err
}

func (r *BentoRequestReconciler) registerYataiComponent() {
	logs := log.Log.WithValues("func", "registerYataiComponent")
	err := r.doRegisterYataiComponent()
	if err != nil {
		logs.Error(err, "registerYataiComponent")
	}
	ticker := time.NewTicker(time.Minute * 5)
	for range ticker.C {
		err := r.doRegisterYataiComponent()
		if err != nil {
			logs.Error(err, "registerYataiComponent")
		}
	}
}

func getJuiceFSStorageClassName() string {
	if v := os.Getenv("JUICEFS_STORAGE_CLASS_NAME"); v != "" {
		return v
	}
	return "juicefs-sc"
}

const (
	trueStr = "true"
)

// SetupWithManager sets up the controller with the Manager.
func (r *BentoRequestReconciler) SetupWithManager(mgr ctrl.Manager) error {
	logs := log.Log.WithValues("func", "SetupWithManager")
	version.Print()

	if os.Getenv("DISABLE_YATAI_COMPONENT_REGISTRATION") != trueStr {
		go r.registerYataiComponent()
	} else {
		logs.Info("yatai component registration is disabled")
	}

	err := ctrl.NewControllerManagedBy(mgr).
		For(&resourcesv1alpha1.BentoRequest{}, builder.WithPredicates(predicate.GenerationChangedPredicate{})).
		Owns(&resourcesv1alpha1.Bento{}).
		Owns(&batchv1.Job{}).
		Complete(r)
	return errors.Wrap(err, "failed to setup BentoRequest controller")
}
