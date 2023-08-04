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
	"strconv"

	"fmt"
	"os"
	"reflect"
	"time"

	"github.com/mitchellh/hashstructure/v2"
	"github.com/pkg/errors"
	"github.com/rs/xid"
	"gopkg.in/yaml.v3"
	corev1 "k8s.io/api/core/v1"
	k8serrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/tools/record"
	"k8s.io/utils/pointer"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
	"sigs.k8s.io/controller-runtime/pkg/log"
	"sigs.k8s.io/controller-runtime/pkg/predicate"

	"bytes"
	"encoding/base64"
	"encoding/json"
	"path/filepath"
	"strings"
	"text/template"

	"github.com/huandu/xstrings"
	"github.com/iancoleman/strcase"
	"github.com/prune998/docker-registry-client/registry"
	"github.com/sirupsen/logrus"
	"k8s.io/apimachinery/pkg/api/resource"
	"k8s.io/client-go/kubernetes"

	commonconfig "github.com/bentoml/yatai-common/config"
	commonconsts "github.com/bentoml/yatai-common/consts"
	"github.com/bentoml/yatai-schemas/modelschemas"
	"github.com/bentoml/yatai-schemas/schemasv1"

	clientconfig "sigs.k8s.io/controller-runtime/pkg/client/config"

	resourcesv1alpha1 "github.com/bentoml/yatai-image-builder/apis/resources/v1alpha1"
	"github.com/bentoml/yatai-image-builder/version"
	yataiclient "github.com/bentoml/yatai-image-builder/yatai-client"

	"github.com/aws/aws-sdk-go/aws"
	"github.com/aws/aws-sdk-go/aws/session"
	"github.com/aws/aws-sdk-go/service/ecr"
)

const (
	KubeAnnotationBentoRequestHash = "yatai.ai/bento-request-hash"
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

	if bentoRequest.Status.Conditions == nil || len(bentoRequest.Status.Conditions) == 0 {
		bentoRequest, err = r.setStatusConditions(ctx, req,
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

	imageExistsCondition := meta.FindStatusCondition(bentoRequest.Status.Conditions, resourcesv1alpha1.BentoRequestConditionTypeImageExists)
	if imageExistsCondition == nil || imageExistsCondition.Status == metav1.ConditionUnknown {
		var imageInfo ImageInfo
		imageInfo, err = r.getImageInfo(ctx, GetImageInfoOption{
			BentoRequest: bentoRequest,
		})
		if err != nil {
			err = errors.Wrap(err, "get image info")
			return
		}

		r.Recorder.Eventf(bentoRequest, corev1.EventTypeNormal, "CheckingImage", "Checking image exists: %s", imageInfo.ImageName)
		var imageExists bool
		imageExists, err = checkImageExists(imageInfo.DockerRegistry, imageInfo.InClusterImageName)
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
			bentoRequest, err = r.setStatusConditions(ctx, req,
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
		} else {
			r.Recorder.Eventf(bentoRequest, corev1.EventTypeNormal, "CheckingImage", "Image not exists: %s", imageInfo.ImageName)
			bentoRequest, err = r.setStatusConditions(ctx, req,
				metav1.Condition{
					Type:    resourcesv1alpha1.BentoRequestConditionTypeImageExists,
					Status:  metav1.ConditionFalse,
					Reason:  "Reconciling",
					Message: fmt.Sprintf("Image %s is not exists", imageInfo.ImageName),
				},
			)
			if err != nil {
				return
			}
		}

		result = ctrl.Result{
			Requeue: true,
		}
		return
	}

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
		result = ctrl.Result{
			Requeue: true,
		}
		return
	}

	var imageInfo ImageInfo
	imageInfo, err = r.getImageInfo(ctx, GetImageInfoOption{
		BentoRequest: bentoRequest,
	})
	if err != nil {
		err = errors.Wrap(err, "get image info")
		return
	}

	imageExists := imageExistsCondition != nil && imageExistsCondition.Status == metav1.ConditionTrue && imageExistsCondition.Message == imageInfo.ImageName
	if !imageExists {
		var hash uint64
		hash, err = hashstructure.Hash(bentoRequest.Spec, hashstructure.FormatV2, nil)
		if err != nil {
			err = errors.Wrap(err, "get bentoRequest CR spec hash")
			return
		}
		hashStr := strconv.FormatUint(hash, 10)

		podLabels := r.getImageBuilderPodLabels(bentoRequest)

		pods := &corev1.PodList{}
		err = r.List(ctx, pods, client.InNamespace(req.Namespace), client.MatchingLabels(podLabels))
		if err != nil {
			err = errors.Wrap(err, "list pods")
			return
		}

		var pod *corev1.Pod
		for _, pod_ := range pods.Items {
			pod_ := pod_

			if pod_.OwnerReferences == nil || len(pod_.OwnerReferences) == 0 {
				continue
			}

			or := pod_.OwnerReferences[0]
			if or.UID != bentoRequest.UID {
				continue
			}

			oldHash := pod_.Annotations[KubeAnnotationBentoRequestHash]
			if oldHash != hashStr {
				err = r.Delete(ctx, &pod_)
				if err != nil {
					err = errors.Wrapf(err, "delete pod %s", pod_.Name)
					return
				}
				result = ctrl.Result{
					Requeue: true,
				}
				return
			} else {
				pod = &pod_
				break
			}
		}

		if pod == nil {
			var imageInfo ImageInfo
			imageInfo, err = r.getImageInfo(ctx, GetImageInfoOption{
				BentoRequest: bentoRequest,
			})
			if err != nil {
				err = errors.Wrap(err, "get image info")
				return
			}

			pod, err = r.generateImageBuilderPod(ctx, GenerateImageBuilderPodOption{
				ImageInfo:    imageInfo,
				BentoRequest: bentoRequest,
			})
			if err != nil {
				err = errors.Wrap(err, "generate image builder pod")
				return
			}
			r.Recorder.Eventf(bentoRequest, corev1.EventTypeNormal, "GenerateImageBuilderPod", "Creating image builder pod: %s", pod.Name)
			err = r.Create(ctx, pod)
			if err != nil {
				err = errors.Wrapf(err, "create image builder pod %s", pod.Name)
				return
			}
			r.Recorder.Eventf(bentoRequest, corev1.EventTypeNormal, "GenerateImageBuilderPod", "Created image builder pod: %s", pod.Name)
			result = ctrl.Result{
				RequeueAfter: 5 * time.Second,
			}
			return
		}

		/*
			Please don't blame me when you see this kind of code,
			this is to avoid "the object has been modified; please apply your changes to the latest version and try again" when updating CR status,
			don't doubt that almost all CRD operators (e.g. cert-manager) can't avoid this stupid error and can only try to avoid this by this stupid way.
		*/
		for i := 0; i < 3; i++ {
			err = r.Get(ctx, req.NamespacedName, bentoRequest)
			if err != nil {
				logs.Error(err, "Failed to re-fetch BentoRequest")
				return
			}

			bentoRequest.Status.ImageBuilderPodStatus = pod.Status

			err = r.Status().Update(ctx, bentoRequest)
			if err != nil {
				time.Sleep(100 * time.Millisecond)
			} else {
				break
			}
		}

		if err != nil {
			logs.Error(err, "Failed to update BentoRequest status")
			return
		}

		if pod.Status.Phase == corev1.PodRunning || pod.Status.Phase == corev1.PodPending {
			err = r.Get(ctx, req.NamespacedName, bentoRequest)
			if err != nil {
				logs.Error(err, "Failed to re-fetch BentoRequest")
				return
			}
			imageBuildingCondition := meta.FindStatusCondition(bentoRequest.Status.Conditions, resourcesv1alpha1.BentoRequestConditionTypeImageBuilding)
			/*
				Please don't blame me when you see this kind of code,
				this is to avoid "the object has been modified; please apply your changes to the latest version and try again" when updating CR status,
				don't doubt that almost all CRD operators (e.g. cert-manager) can't avoid this stupid error and can only try to avoid this by this stupid way.
			*/
			for i := 0; i < 3; i++ {
				err = r.Get(ctx, req.NamespacedName, bentoRequest)
				if err != nil {
					logs.Error(err, "Failed to re-fetch BentoRequest")
					return
				}
				if pod.Status.Phase == corev1.PodRunning {
					meta.SetStatusCondition(&bentoRequest.Status.Conditions, metav1.Condition{
						Type:    resourcesv1alpha1.BentoRequestConditionTypeImageBuilding,
						Status:  metav1.ConditionTrue,
						Reason:  "Reconciling",
						Message: fmt.Sprintf("Image builder pod %s status is %s", pod.Name, pod.Status.Phase),
					})
				} else {
					meta.SetStatusCondition(&bentoRequest.Status.Conditions, metav1.Condition{
						Type:    resourcesv1alpha1.BentoRequestConditionTypeImageBuilding,
						Status:  metav1.ConditionUnknown,
						Reason:  "Reconciling",
						Message: fmt.Sprintf("Image builder pod %s status is %s", pod.Name, pod.Status.Phase),
					})
				}
				bentoRequest.Status.ImageBuilderPodStatus = pod.Status
				if bentoRequest.Spec.ImageBuildTimeout != nil {
					if imageBuildingCondition != nil && imageBuildingCondition.LastTransitionTime.Add(*bentoRequest.Spec.ImageBuildTimeout).Before(time.Now()) {
						meta.SetStatusCondition(&bentoRequest.Status.Conditions, metav1.Condition{
							Type:    resourcesv1alpha1.BentoRequestConditionTypeImageBuilding,
							Status:  metav1.ConditionFalse,
							Reason:  "Timeout",
							Message: fmt.Sprintf("Image builder pod %s status is %s", pod.Name, pod.Status.Phase),
						})
						err = errors.New("image build timeout")
						return
					}
				}

				err = r.Status().Update(ctx, bentoRequest)
				if err != nil {
					time.Sleep(100 * time.Millisecond)
				} else {
					break
				}
			}

			if err != nil {
				logs.Error(err, "Failed to update BentoRequest status")
				return
			}

			if imageBuildingCondition != nil && imageBuildingCondition.Status != metav1.ConditionTrue && pod.Status.Phase == corev1.PodRunning {
				r.Recorder.Eventf(bentoRequest, corev1.EventTypeNormal, "BentoImageBuilder", "Image is building now")
			}

			result = ctrl.Result{
				RequeueAfter: 10 * time.Second,
			}

			return
		}

		if pod.Status.Phase != corev1.PodSucceeded {
			bentoRequest, err = r.setStatusConditions(ctx, req,
				metav1.Condition{
					Type:    resourcesv1alpha1.BentoRequestConditionTypeImageBuilding,
					Status:  metav1.ConditionFalse,
					Reason:  "Reconciling",
					Message: fmt.Sprintf("Image builder pod %s status is %s", pod.Name, pod.Status.Phase),
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
				Message: fmt.Sprintf("Image builder pod %s status is %s", pod.Name, pod.Status.Phase),
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

		result = ctrl.Result{
			Requeue: true,
		}

		return
	}

	bentoCR := &resourcesv1alpha1.Bento{
		ObjectMeta: metav1.ObjectMeta{
			Name:      bentoRequest.Name,
			Namespace: bentoRequest.Namespace,
		},
		Spec: resourcesv1alpha1.BentoSpec{
			Tag:     bentoRequest.Spec.BentoTag,
			Image:   imageInfo.ImageName,
			Context: bentoRequest.Spec.Context,
			Runners: bentoRequest.Spec.Runners,
		},
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
	return os.Getenv(commonconsts.EnvAWSECRWithIAMRole) == "true"
}

func GetAWSECRRegion() string {
	return os.Getenv(commonconsts.EnvAWSECRRegion)
}

func CheckECRImageExists(imageName string) (bool, error) {
	region := GetAWSECRRegion()
	if region == "" {
		return false, fmt.Errorf("%s is not set", commonconsts.EnvAWSECRRegion)
	}
	sess, err := session.NewSession(&aws.Config{
		Region: aws.String(region)},
	)
	if err != nil {
		err = errors.Wrap(err, "create aws session")
		return false, err
	}

	_, _, imageName_ := xstrings.Partition(imageName, "/")
	repoName, _, tag := xstrings.Partition(imageName_, ":")

	svc := ecr.New(sess)
	input := &ecr.ListImagesInput{
		RepositoryName: aws.String(repoName),
	}

	result, err := svc.ListImages(input)
	if err != nil {
		err = errors.Wrap(err, "list ECR images")
		return false, err
	}

	for _, image := range result.ImageIds {
		if image.ImageTag != nil && *image.ImageTag == tag {
			return true, nil
		}
	}

	return false, nil
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

func MakeSureDockerConfigJSONSecret(ctx context.Context, kubeCli *kubernetes.Clientset, namespace string, dockerRegistryConf *commonconfig.DockerRegistryConfig) (dockerConfigJSONSecret *corev1.Secret, err error) {
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

	secretsCli := kubeCli.CoreV1().Secrets(namespace)

	dockerConfigJSONSecret, err = secretsCli.Get(ctx, dockerConfigSecretName, metav1.GetOptions{})
	dockerConfigIsNotFound := k8serrors.IsNotFound(err)
	// nolint: gocritic
	if err != nil && !dockerConfigIsNotFound {
		err = errors.Wrap(err, "get docker config secret")
		return nil, err
	}
	err = nil
	if dockerConfigIsNotFound {
		dockerConfigJSONSecret = &corev1.Secret{
			Type:       corev1.SecretTypeDockerConfigJson,
			ObjectMeta: metav1.ObjectMeta{Name: dockerConfigSecretName},
			Data: map[string][]byte{
				".dockerconfigjson": dockerConfigContent,
			},
		}
		_, err_ := secretsCli.Create(ctx, dockerConfigJSONSecret, metav1.CreateOptions{})
		if err_ != nil {
			dockerConfigJSONSecret, err = secretsCli.Get(ctx, dockerConfigSecretName, metav1.GetOptions{})
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
		_, err = secretsCli.Update(ctx, dockerConfigJSONSecret, metav1.UpdateOptions{})
		if err != nil {
			err = errors.Wrap(err, "update docker config secret")
			return nil, err
		}
	}

	return
}

func getYataiClient(ctx context.Context) (yataiClient **yataiclient.YataiClient, yataiConf **commonconfig.YataiConfig, err error) {
	restConfig := clientconfig.GetConfigOrDie()
	clientset, err := kubernetes.NewForConfig(restConfig)
	if err != nil {
		err = errors.Wrap(err, "create kubernetes clientset")
		return
	}

	yataiConf_, err := commonconfig.GetYataiConfig(ctx, clientset, commonconsts.YataiImageBuilderComponentName, true)
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

func getDockerRegistry(ctx context.Context, bentoRequest *resourcesv1alpha1.BentoRequest, cliset *kubernetes.Clientset) (dockerRegistry modelschemas.DockerRegistrySchema, err error) {
	if bentoRequest != nil && bentoRequest.Spec.DockerConfigJSONSecretName != "" {
		var secret *corev1.Secret
		secret, err = cliset.CoreV1().Secrets(bentoRequest.Namespace).Get(ctx, bentoRequest.Spec.DockerConfigJSONSecretName, metav1.GetOptions{})
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

	dockerRegistryConfig, err := commonconfig.GetDockerRegistryConfig(ctx, cliset)
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

func getBentoImageName(bentoRequest *resourcesv1alpha1.BentoRequest, dockerRegistry modelschemas.DockerRegistrySchema, bentoRepositoryName, bentoVersion string, inCluster bool) string {
	if bentoRequest != nil && bentoRequest.Spec.Image != "" {
		return bentoRequest.Spec.Image
	}
	var imageName string
	if inCluster {
		imageName = fmt.Sprintf("%s:yatai.%s.%s", dockerRegistry.BentosRepositoryURIInCluster, bentoRepositoryName, bentoVersion)
	} else {
		imageName = fmt.Sprintf("%s:yatai.%s.%s", dockerRegistry.BentosRepositoryURI, bentoRepositoryName, bentoVersion)
	}
	return imageName
}

func checkImageExists(dockerRegistry modelschemas.DockerRegistrySchema, imageName string) (bool, error) {
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
	restConfig := clientconfig.GetConfigOrDie()
	kubeCli, err := kubernetes.NewForConfig(restConfig)
	if err != nil {
		err = errors.Wrap(err, "create kubernetes clientset")
		return
	}
	dockerRegistry, err := getDockerRegistry(ctx, opt.BentoRequest, kubeCli)
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
		dockerRegistryConf, err = commonconfig.GetDockerRegistryConfig(ctx, kubeCli)
		if err != nil {
			err = errors.Wrap(err, "get docker registry")
			return
		}
		imageInfo.DockerRegistryInsecure = !dockerRegistryConf.Secure
		var dockerConfigSecret *corev1.Secret
		r.Recorder.Eventf(opt.BentoRequest, corev1.EventTypeNormal, "GenerateImageBuilderPod", "Making sure docker config secret %s in namespace %s", commonconsts.KubeSecretNameRegcred, opt.BentoRequest.Namespace)
		dockerConfigSecret, err = MakeSureDockerConfigJSONSecret(ctx, kubeCli, opt.BentoRequest.Namespace, dockerRegistryConf)
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

	yataiClient_, _, err := getYataiClient(ctx)
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

func (r *BentoRequestReconciler) getImageBuilderPodName() string {
	guid := xid.New()
	return fmt.Sprintf("yatai-bento-image-builder-%s", guid.String())
}

func (r *BentoRequestReconciler) getImageBuilderPodLabels(bentoRequest *resourcesv1alpha1.BentoRequest) map[string]string {
	bentoRepositoryName, _, bentoVersion := xstrings.Partition(bentoRequest.Spec.BentoTag, ":")
	return map[string]string{
		commonconsts.KubeLabelIsBentoImageBuilder:  "true",
		commonconsts.KubeLabelYataiBentoRepository: bentoRepositoryName,
		commonconsts.KubeLabelYataiBento:           bentoVersion,
	}
}

type GenerateImageBuilderPodOption struct {
	ImageInfo    ImageInfo
	BentoRequest *resourcesv1alpha1.BentoRequest
}

func (r *BentoRequestReconciler) generateImageBuilderPod(ctx context.Context, opt GenerateImageBuilderPodOption) (pod *corev1.Pod, err error) {
	bentoRepositoryName, _, bentoVersion := xstrings.Partition(opt.BentoRequest.Spec.BentoTag, ":")
	kubeName := r.getImageBuilderPodName()
	kubeLabels := r.getImageBuilderPodLabels(opt.BentoRequest)
	restConfig := clientconfig.GetConfigOrDie()
	kubeCli, err := kubernetes.NewForConfig(restConfig)
	if err != nil {
		err = errors.Wrap(err, "create kubernetes clientset")
		return
	}

	inClusterImageName := opt.ImageInfo.InClusterImageName

	logs := log.FromContext(ctx)
	logs.Info(fmt.Sprintf("Generating image builder pod %s", kubeName))

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

		yataiClient_, yataiConf_, err = getYataiClient(ctx)
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
				Name: yataiAPITokenSecretName,
			},
			StringData: map[string]string{
				commonconsts.EnvYataiApiToken: yataiConf.ApiToken,
			},
		}

		r.Recorder.Eventf(opt.BentoRequest, corev1.EventTypeNormal, "GenerateImageBuilderPod", "Getting secret %s in namespace %s", yataiAPITokenSecretName, opt.BentoRequest.Namespace)
		_, err = kubeCli.CoreV1().Secrets(opt.BentoRequest.Namespace).Get(ctx, yataiAPITokenSecretName, metav1.GetOptions{})
		isNotFound := k8serrors.IsNotFound(err)
		if err != nil && !isNotFound {
			err = errors.Wrapf(err, "failed to get secret %s", yataiAPITokenSecretName)
			return
		}

		if isNotFound {
			r.Recorder.Eventf(opt.BentoRequest, corev1.EventTypeNormal, "GenerateImageBuilderPod", "Secret %s is not found, so creating it in namespace %s", yataiAPITokenSecretName, opt.BentoRequest.Namespace)
			_, err = kubeCli.CoreV1().Secrets(opt.BentoRequest.Namespace).Create(ctx, yataiAPITokenSecret, metav1.CreateOptions{})
			isExists := k8serrors.IsAlreadyExists(err)
			if err != nil && !isExists {
				err = errors.Wrapf(err, "failed to create secret %s", yataiAPITokenSecretName)
				return
			}
			r.Recorder.Eventf(opt.BentoRequest, corev1.EventTypeNormal, "GenerateImageBuilderPod", "Secret %s is created in namespace %s", yataiAPITokenSecretName, opt.BentoRequest.Namespace)
		} else {
			r.Recorder.Eventf(opt.BentoRequest, corev1.EventTypeNormal, "GenerateImageBuilderPod", "Secret %s is found in namespace %s, so updating it", yataiAPITokenSecretName, opt.BentoRequest.Namespace)
			_, err = kubeCli.CoreV1().Secrets(opt.BentoRequest.Namespace).Update(ctx, yataiAPITokenSecret, metav1.UpdateOptions{})
			if err != nil {
				err = errors.Wrapf(err, "failed to update secret %s", yataiAPITokenSecretName)
				return
			}
			r.Recorder.Eventf(opt.BentoRequest, corev1.EventTypeNormal, "GenerateImageBuilderPod", "Secret %s is updated in namespace %s", yataiAPITokenSecretName, opt.BentoRequest.Namespace)
		}
	}

	// nolint: gosec
	var awsAccessKeySecretName string
	awsAccessKeyID := os.Getenv(commonconsts.EnvAWSAccessKeyID)
	awsSecretAccessKey := os.Getenv(commonconsts.EnvAWSSecretAccessKey)
	if awsAccessKeyID != "" && awsSecretAccessKey != "" {
		// nolint: gosec
		awsAccessKeySecretName = "yatai-image-builder-aws-access-key"
		stringData := map[string]string{
			commonconsts.EnvAWSAccessKeyID:     awsAccessKeyID,
			commonconsts.EnvAWSSecretAccessKey: awsSecretAccessKey,
			"AWS_EC2_METADATA_DISABLED":        "true",
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

	internalImages := commonconfig.GetInternalImages()
	logrus.Infof("Image builder is using the images %v", *internalImages)

	buildEngine := getBentoImageBuildEngine()

	privileged := buildEngine != BentoImageBuildEngineBuildkitRootless

	bentoDownloadCommandTemplate, err := template.New("downloadCommand").Parse(`
set -e

mkdir -p /workspace/buildcontext
url="{{.BentoDownloadURL}}"
echo "Downloading bento {{.BentoRepositoryName}}:{{.BentoVersion}} tar file from ${url} to /tmp/downloaded.tar..."
if [[ ${url} == s3://* ]]; then
	echo "Downloading from s3..."
	aws s3 cp ${url} /tmp/downloaded.tar
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

	if awsAccessKeySecretName != "" {
		downloaderContainerEnvFrom = append(downloaderContainerEnvFrom, corev1.EnvFromSource{
			SecretRef: &corev1.SecretEnvSource{
				LocalObjectReference: corev1.LocalObjectReference{
					Name: awsAccessKeySecretName,
				},
			},
		})
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
		},
	}

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
		modelRepositoryName, _, modelVersion := xstrings.Partition(model.Tag, ":")
		modelDownloadURL := model.DownloadURL
		modelDownloadHeader := ""
		if modelDownloadURL == "" {
			if bento == nil {
				continue
			}

			var yataiClient_ **yataiclient.YataiClient
			var yataiConf_ **commonconfig.YataiConfig

			yataiClient_, yataiConf_, err = getYataiClient(ctx)
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
echo "Downloading model {{.ModelRepositoryName}}:{{.ModelVersion}} tar file from ${url} to /tmp/downloaded.tar..."
if [[ ${url} == s3://* ]]; then
	echo "Downloading from s3..."
	aws s3 cp ${url} /tmp/downloaded.tar
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
echo "Done"
`)).Execute(&modelDownloadCommandOutput, map[string]interface{}{
			"ModelDirPath":           modelDirPath,
			"ModelDownloadURL":       modelDownloadURL,
			"ModelDownloadHeader":    modelDownloadHeader,
			"ModelRepositoryDirPath": modelRepositoryDirPath,
			"ModelRepositoryName":    modelRepositoryName,
			"ModelVersion":           modelVersion,
			"Privileged":             privileged,
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
		})
	}

	var globalExtraPodMetadata *resourcesv1alpha1.ExtraPodMetadata
	var globalExtraPodSpec *resourcesv1alpha1.ExtraPodSpec
	var globalExtraContainerEnv []corev1.EnvVar
	var globalDefaultImageBuilderContainerResources *corev1.ResourceRequirements
	var buildArgs []string
	var builderArgs []string

	configNamespace, err := commonconfig.GetYataiImageBuilderNamespace(ctx, kubeCli)
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

		globalDefaultImageBuilderContainerResources = &corev1.ResourceRequirements{}

		if val, ok := configCm.Data["default_image_builder_container_resources"]; ok {
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

	dockerFilePath := "/workspace/buildcontext/env/docker/Dockerfile"

	envs := []corev1.EnvVar{
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
		envs = append(envs, corev1.EnvVar{
			Name:  "AWS_REGION",
			Value: GetAWSECRRegion(),
		}, corev1.EnvVar{
			Name:  "AWS_SDK_LOAD_CONFIG",
			Value: "true",
		})
	}

	if !privileged {
		envs = append(envs, corev1.EnvVar{
			Name:  "BUILDKITD_FLAGS",
			Value: "--oci-worker-no-process-sandbox",
		})
	}

	kubeAnnotations := make(map[string]string)
	var hash uint64
	hash, err = hashstructure.Hash(opt.BentoRequest.Spec, hashstructure.FormatV2, nil)
	if err != nil {
		err = errors.Wrap(err, "get bentoRequest CR spec hash")
		return
	}
	hashStr := strconv.FormatUint(hash, 10)
	kubeAnnotations[KubeAnnotationBentoRequestHash] = hashStr
	var command []string
	args := []string{
		"--context=/workspace/buildcontext",
		"--verbosity=info",
		fmt.Sprintf("--dockerfile=%s", dockerFilePath),
		fmt.Sprintf("--insecure=%v", dockerRegistryInsecure),
		fmt.Sprintf("--destination=%s", inClusterImageName),
	}
	var builderImage string
	switch buildEngine {
	case BentoImageBuildEngineKaniko:
		builderImage = internalImages.Kaniko
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
			fmt.Sprintf("type=image,name=%s,push=true,registry.insecure=%v", inClusterImageName, dockerRegistryInsecure),
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
		if isBuildkit {
			args = append(args, "--opt", fmt.Sprintf("build-arg:%s", buildArg))
		} else {
			args = append(args, fmt.Sprintf("--build-arg=%s", buildArg))
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
			_, err = kubeCli.CoreV1().Secrets(opt.BentoRequest.Namespace).Get(ctx, buildArgsSecretName, metav1.GetOptions{})
			localBuildArgsSecretIsNotFound := k8serrors.IsNotFound(err)
			if err != nil && !localBuildArgsSecretIsNotFound {
				err = errors.Wrapf(err, "failed to get secret %s from namespace %s", buildArgsSecretName, opt.BentoRequest.Namespace)
				return
			}
			if localBuildArgsSecretIsNotFound {
				r.Recorder.Eventf(opt.BentoRequest, corev1.EventTypeNormal, "GenerateImageBuilderPod", "Copying secret %s from namespace %s to namespace %s", buildArgsSecretName, configNamespace, opt.BentoRequest.Namespace)
				_, err = kubeCli.CoreV1().Secrets(opt.BentoRequest.Namespace).Create(ctx, &corev1.Secret{
					ObjectMeta: metav1.ObjectMeta{
						Name: buildArgsSecretName,
					},
					Data: buildArgsSecret.Data,
				}, metav1.CreateOptions{})
				if err != nil {
					err = errors.Wrapf(err, "failed to create secret %s in namespace %s", buildArgsSecretName, opt.BentoRequest.Namespace)
					return
				}
			} else {
				r.Recorder.Eventf(opt.BentoRequest, corev1.EventTypeNormal, "GenerateImageBuilderPod", "Secret %s is already in namespace %s", buildArgsSecretName, opt.BentoRequest.Namespace)
				r.Recorder.Eventf(opt.BentoRequest, corev1.EventTypeNormal, "GenerateImageBuilderPod", "Updating secret %s in namespace %s", buildArgsSecretName, opt.BentoRequest.Namespace)
				_, err = kubeCli.CoreV1().Secrets(opt.BentoRequest.Namespace).Update(ctx, &corev1.Secret{
					ObjectMeta: metav1.ObjectMeta{
						Name: buildArgsSecretName,
					},
					Data: buildArgsSecret.Data,
				}, metav1.UpdateOptions{})
				if err != nil {
					err = errors.Wrapf(err, "failed to update secret %s in namespace %s", buildArgsSecretName, opt.BentoRequest.Namespace)
					return
				}
			}
		}

		for key := range buildArgsSecret.Data {
			envName := fmt.Sprintf("BENTOML_BUILD_ARG_%s", strings.ReplaceAll(strings.ToUpper(strcase.ToKebab(key)), "-", "_"))
			envs = append(envs, corev1.EnvVar{
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

	container := corev1.Container{
		Name:            "builder",
		Image:           builderImage,
		ImagePullPolicy: corev1.PullAlways,
		Command:         command,
		Args:            args,
		VolumeMounts:    volumeMounts,
		Env:             envs,
		TTY:             true,
		Stdin:           true,
		SecurityContext: builderContainerSecurityContext,
		Resources: corev1.ResourceRequirements{
			Requests: corev1.ResourceList{
				corev1.ResourceCPU:    resource.MustParse("500m"),
				corev1.ResourceMemory: resource.MustParse("1Gi"),
			},
			Limits: corev1.ResourceList{
				corev1.ResourceCPU:    resource.MustParse("1"),
				corev1.ResourceMemory: resource.MustParse("3Gi"),
			},
		},
	}

	if globalDefaultImageBuilderContainerResources != nil {
		container.Resources = *globalDefaultImageBuilderContainerResources
	}

	if opt.BentoRequest.Spec.ImageBuilderContainerResources != nil {
		container.Resources = *opt.BentoRequest.Spec.ImageBuilderContainerResources
	}

	pod = &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Name:        kubeName,
			Namespace:   opt.BentoRequest.Namespace,
			Labels:      kubeLabels,
			Annotations: kubeAnnotations,
		},
		Spec: corev1.PodSpec{
			RestartPolicy:  corev1.RestartPolicyNever,
			Volumes:        volumes,
			InitContainers: initContainers,
			Containers: []corev1.Container{
				container,
			},
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

	err = ctrl.SetControllerReference(opt.BentoRequest, pod, r.Scheme)
	if err != nil {
		err = errors.Wrapf(err, "set controller reference for pod %s", pod.Name)
		return
	}

	return
}

func (r *BentoRequestReconciler) doRegisterYataiComponent() (err error) {
	logs := log.Log.WithValues("func", "doRegisterYataiComponent")

	ctx, cancel := context.WithTimeout(context.TODO(), time.Minute*5)
	defer cancel()

	logs.Info("getting yatai client")
	yataiClient, yataiConf, err := getYataiClient(ctx)
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

	restConf := clientconfig.GetConfigOrDie()
	cliset, err := kubernetes.NewForConfig(restConf)
	if err != nil {
		err = errors.Wrapf(err, "create kubernetes client for %s", restConf.Host)
		return
	}

	namespace, err := commonconfig.GetYataiImageBuilderNamespace(ctx, cliset)
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

	pred := predicate.GenerationChangedPredicate{}
	err := ctrl.NewControllerManagedBy(mgr).
		For(&resourcesv1alpha1.BentoRequest{}).
		Owns(&resourcesv1alpha1.Bento{}).
		Owns(&corev1.Pod{}).
		WithEventFilter(pred).
		Complete(r)
	return errors.Wrap(err, "failed to setup BentoRequest controller")
}
