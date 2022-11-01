package services

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"path/filepath"
	"strings"
	"text/template"

	"github.com/banzaicloud/k8s-objectmatcher/patch"
	"github.com/go-logr/logr"
	"github.com/huandu/xstrings"
	"github.com/iancoleman/strcase"
	"github.com/pkg/errors"
	"github.com/prune998/docker-registry-client/registry"
	"github.com/sirupsen/logrus"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/tools/record"
	"sigs.k8s.io/controller-runtime/pkg/client/config"

	commonconfig "github.com/bentoml/yatai-common/config"
	commonconsts "github.com/bentoml/yatai-common/consts"
	"github.com/bentoml/yatai-schemas/modelschemas"
	"github.com/bentoml/yatai-schemas/schemasv1"

	resourcesv1alpha1 "github.com/bentoml/yatai-image-builder/apis/resources/v1alpha1"
	yataiclient "github.com/bentoml/yatai-image-builder/yatai-client"
	clientconfig "sigs.k8s.io/controller-runtime/pkg/client/config"
)

type imageBuilderService struct{}

var ImageBuilderService = &imageBuilderService{}

func MakeSureDockerConfigJsonSecret(ctx context.Context, kubeCli *kubernetes.Clientset, namespace string, dockerRegistryConf *commonconfig.DockerRegistryConfig) (dockerConfigJsonSecret *corev1.Secret, err error) {
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
		return nil, err
	}

	secretsCli := kubeCli.CoreV1().Secrets(namespace)

	dockerConfigJsonSecret, err = secretsCli.Get(ctx, dockerConfigSecretName, metav1.GetOptions{})
	dockerConfigIsNotFound := apierrors.IsNotFound(err)
	// nolint: gocritic
	if err != nil && !dockerConfigIsNotFound {
		return nil, err
	}
	err = nil
	if dockerConfigIsNotFound {
		dockerConfigJsonSecret = &corev1.Secret{
			Type: corev1.SecretTypeDockerConfigJson,
			ObjectMeta: metav1.ObjectMeta{Name: dockerConfigSecretName},
			Data: map[string][]byte{
				".dockerconfigjson": dockerConfigContent,
			},
		}
		_, err_ := secretsCli.Create(ctx, dockerConfigJsonSecret, metav1.CreateOptions{})
		if err_ != nil {
			dockerConfigJsonSecret, err = secretsCli.Get(ctx, dockerConfigSecretName, metav1.GetOptions{})
			dockerConfigIsNotFound = apierrors.IsNotFound(err)
			if err != nil && !dockerConfigIsNotFound {
				return nil, err
			}
			if dockerConfigIsNotFound {
				return nil, err_
			}
			if err != nil {
				err = nil
			}
		}
	} else {
		dockerConfigJsonSecret.Data[".dockerconfigjson"] = dockerConfigContent
		_, err = secretsCli.Update(ctx, dockerConfigJsonSecret, metav1.UpdateOptions{})
		if err != nil {
			return nil, err
		}
	}

	return
}

func getYataiClient(ctx context.Context) (yataiClient *yataiclient.YataiClient, yataiConf *commonconfig.YataiConfig, err error) {
	restConfig := clientconfig.GetConfigOrDie()
	clientset, err := kubernetes.NewForConfig(restConfig)
	if err != nil {
		err = errors.Wrap(err, "create kubernetes clientset")
		return
	}

	yataiConf, err = commonconfig.GetYataiConfig(ctx, clientset, commonconsts.KubeSecretNameYataiImageBuilderSharedEnv, false)
	if err != nil {
		err = errors.Wrap(err, "get yatai config")
		return
	}

	if yataiConf.ClusterName == "" {
		yataiConf.ClusterName = "default"
	}
	yataiClient = yataiclient.NewYataiClient(yataiConf.Endpoint, fmt.Sprintf("%s:%s:%s", commonconsts.YataiImageBuilderComponentName, yataiConf.ClusterName, yataiConf.ApiToken))
	return
}

func getDockerRegistry(ctx context.Context, cliset *kubernetes.Clientset) (dockerRegistry modelschemas.DockerRegistrySchema, err error) {
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

func getBentoImageName(dockerRegistry modelschemas.DockerRegistrySchema, bentoRepositoryName, bentoVersion string, inCluster bool) string {
	var imageName string
	if inCluster {
		imageName = fmt.Sprintf("%s:yatai.%s.%s", dockerRegistry.BentosRepositoryURIInCluster, bentoRepositoryName, bentoVersion)
	} else {
		imageName = fmt.Sprintf("%s:yatai.%s.%s", dockerRegistry.BentosRepositoryURI, bentoRepositoryName, bentoVersion)
	}
	return imageName
}

func checkImageExists(dockerRegistry modelschemas.DockerRegistrySchema, imageName string) (bool, error) {
	server, _, imageName := xstrings.Partition(imageName, "/")
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

type CreateImageBuilderPodOption struct {
	BentoRequest *resourcesv1alpha1.BentoRequest
	EventRecorder   record.EventRecorder
	Logger logr.Logger
	RecreateIfFailed bool
}

func (s *imageBuilderService) CreateImageBuilderPod(ctx context.Context, opt CreateImageBuilderPodOption) (pod *corev1.Pod, bento *schemasv1.BentoFullSchema, imageName, dockerConfigJsonSecretName string, err error) {
	bentoRepositoryName, _, bentoVersion := xstrings.Partition(opt.BentoRequest.Spec.BentoTag, ":")
	kubeName := strcase.ToKebab(fmt.Sprintf("yatai-bento-image-builder-%s", opt.BentoRequest.Name))
	kubeLabels := map[string]string{
		commonconsts.KubeLabelIsBentoImageBuilder: "true",
		commonconsts.KubeLabelYataiBentoRepository: bentoRepositoryName,
		commonconsts.KubeLabelYataiBento:           bentoVersion,
	}
	restConfig := config.GetConfigOrDie()
	kubeCli, err := kubernetes.NewForConfig(restConfig)
	if err != nil {
		err = errors.Wrap(err, "create kubernetes clientset")
		return
	}

	dockerRegistry, err := getDockerRegistry(ctx, kubeCli)
	if err != nil {
		err = errors.Wrap(err, "get docker registry")
		return
	}

	imageName = getBentoImageName(dockerRegistry, bentoRepositoryName, bentoVersion, false)
	inClusterImageName := getBentoImageName(dockerRegistry, bentoRepositoryName, bentoVersion, true)

	opt.EventRecorder.Eventf(opt.BentoRequest, corev1.EventTypeNormal, "CreateImageBuilderPod", "Checking image exists: %s", imageName)
	imageExists, err := checkImageExists(dockerRegistry, inClusterImageName)
	if err != nil {
		err = errors.Wrapf(err, "check image %s exists", imageName)
		return
	}

	if imageExists {
		opt.EventRecorder.Eventf(opt.BentoRequest, corev1.EventTypeNormal, "CreateImageBuilderPod", "Image exists: %s", imageName)

		if opt.BentoRequest.Spec.DownloadURL == "" {
			var yataiClient *yataiclient.YataiClient
			var yataiConf *commonconfig.YataiConfig

			yataiClient, yataiConf, err = getYataiClient(ctx)
			if err != nil {
				err = errors.Wrap(err, "get yatai client")
				return
			}

			if yataiConf.Endpoint == "" {
				err = errors.New("yatai endpoint is empty")
				return
			}

			opt.EventRecorder.Eventf(opt.BentoRequest, corev1.EventTypeNormal, "CreateImageBuilderPod", "Getting bento %s from yatai service", opt.BentoRequest.Spec.BentoTag)
			bento, err = yataiClient.GetBento(ctx, bentoRepositoryName, bentoVersion)
			if err != nil {
				err = errors.Wrap(err, "get bento")
				return
			}
			opt.EventRecorder.Eventf(opt.BentoRequest, corev1.EventTypeNormal, "CreateImageBuilderPod", "Got bento %s from yatai service", opt.BentoRequest.Spec.BentoTag)
		}
		return
	}

	logs := opt.Logger.WithValues("imageName", inClusterImageName)
	logs.Info(fmt.Sprintf("Creating image builder pod %s", kubeName))

	dockerConfigJsonSecretName = opt.BentoRequest.Spec.DockerConfigJsonSecretName

	dockerRegistryInsecure := opt.BentoRequest.Annotations[commonconsts.KubeAnnotationDockerRegistryInsecure] == "true"

	if dockerConfigJsonSecretName == "" {
		var dockerRegistryConf *commonconfig.DockerRegistryConfig
		dockerRegistryConf, err = commonconfig.GetDockerRegistryConfig(ctx, kubeCli)
		if err != nil {
			err = errors.Wrap(err, "get docker registry")
			return
		}
		dockerRegistryInsecure = !dockerRegistryConf.Secure
		var dockerConfigSecret *corev1.Secret
		opt.EventRecorder.Eventf(opt.BentoRequest, corev1.EventTypeNormal, "CreateImageBuilderPod", "Making sure docker config secret %s in namespace %s", commonconsts.KubeSecretNameRegcred, opt.BentoRequest.Namespace)
		dockerConfigSecret, err = MakeSureDockerConfigJsonSecret(ctx, kubeCli, opt.BentoRequest.Namespace, dockerRegistryConf)
		if err != nil {
			err = errors.Wrap(err, "make sure docker config secret")
			return
		}
		opt.EventRecorder.Eventf(opt.BentoRequest, corev1.EventTypeNormal, "CreateImageBuilderPod", "Docker config secret %s in namespace %s is ready", commonconsts.KubeSecretNameRegcred, opt.BentoRequest.Namespace)
		if dockerConfigSecret != nil {
			dockerConfigJsonSecretName = dockerConfigSecret.Name
		}
	}

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

	if dockerConfigJsonSecretName != "" {
		volumes = append(volumes, corev1.Volume{
			Name: dockerConfigJsonSecretName,
			VolumeSource: corev1.VolumeSource{
				Secret: &corev1.SecretVolumeSource{
					SecretName: dockerConfigJsonSecretName,
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
			Name:      dockerConfigJsonSecretName,
			MountPath: "/kaniko/.docker/",
		})
	}

	yataiApiTokenSecretName := ""
	bentoDownloadURL := opt.BentoRequest.Spec.DownloadURL
	bentoDownloadHeader := ""

	if bentoDownloadURL == "" {
		var yataiClient *yataiclient.YataiClient
		var yataiConf *commonconfig.YataiConfig

		yataiClient, yataiConf, err = getYataiClient(ctx)
		if err != nil {
			err = errors.Wrap(err, "get yatai client")
			return
		}

		if yataiConf.Endpoint == "" {
			err = errors.New("yatai endpoint is empty")
			return
		}

		opt.EventRecorder.Eventf(opt.BentoRequest, corev1.EventTypeNormal, "CreateImageBuilderPod", "Getting bento %s from yatai service", opt.BentoRequest.Spec.BentoTag)
		bento, err = yataiClient.GetBento(ctx, bentoRepositoryName, bentoVersion)
		if err != nil {
			err = errors.Wrap(err, "get bento")
			return
		}
		opt.EventRecorder.Eventf(opt.BentoRequest, corev1.EventTypeNormal, "CreateImageBuilderPod", "Got bento %s from yatai service", opt.BentoRequest.Spec.BentoTag)

		if bento.TransmissionStrategy == modelschemas.TransmissionStrategyPresignedURL {
			var bento_ *schemasv1.BentoSchema
			opt.EventRecorder.Eventf(opt.BentoRequest, corev1.EventTypeNormal, "CreateImageBuilderPod", "Getting presigned url for bento %s from yatai service", opt.BentoRequest.Spec.BentoTag)
			bento_, err = yataiClient.PresignBentoDownloadURL(ctx, bentoRepositoryName, bentoVersion)
			if err != nil {
				err = errors.Wrap(err, "presign bento download url")
				return
			}
			opt.EventRecorder.Eventf(opt.BentoRequest, corev1.EventTypeNormal, "CreateImageBuilderPod", "Got presigned url for bento %s from yatai service", opt.BentoRequest.Spec.BentoTag)
			bentoDownloadURL = bento_.PresignedDownloadUrl
		} else {
			bentoDownloadURL = fmt.Sprintf("%s/api/v1/bento_repositories/%s/bentos/%s/download", yataiConf.Endpoint, bentoRepositoryName, bentoVersion)
			bentoDownloadHeader = fmt.Sprintf("%s: %s:%s:$%s", commonconsts.YataiApiTokenHeaderName, commonconsts.YataiImageBuilderComponentName, yataiConf.ClusterName, commonconsts.EnvYataiApiToken)
		}

		yataiApiTokenSecretName = "yatai-api-token"

		yataiApiTokenSecret := &corev1.Secret{
			ObjectMeta: metav1.ObjectMeta{
				Name: yataiApiTokenSecretName,
			},
			StringData: map[string]string{
				commonconsts.EnvYataiApiToken: yataiConf.ApiToken,
			},
		}

		opt.EventRecorder.Eventf(opt.BentoRequest, corev1.EventTypeNormal, "CreateImageBuilderPod", "Getting secret %s in namespace %s", yataiApiTokenSecretName, opt.BentoRequest.Namespace)
		_, err = kubeCli.CoreV1().Secrets(opt.BentoRequest.Namespace).Get(ctx, yataiApiTokenSecretName, metav1.GetOptions{})
		isNotFound := apierrors.IsNotFound(err)
		if err != nil && !isNotFound {
			err = errors.Wrapf(err, "failed to get secret %s", yataiApiTokenSecretName)
			return
		}

		if isNotFound {
			opt.EventRecorder.Eventf(opt.BentoRequest, corev1.EventTypeNormal, "CreateImageBuilderPod", "Secret %s is not found, so creating it in namespace %s", yataiApiTokenSecretName, opt.BentoRequest.Namespace)
			_, err = kubeCli.CoreV1().Secrets(opt.BentoRequest.Namespace).Create(ctx, yataiApiTokenSecret, metav1.CreateOptions{})
			isExists := apierrors.IsAlreadyExists(err)
			if err != nil && !isExists {
				err = errors.Wrapf(err, "failed to create secret %s", yataiApiTokenSecretName)
				return
			}
			opt.EventRecorder.Eventf(opt.BentoRequest, corev1.EventTypeNormal, "CreateImageBuilderPod", "Secret %s is created in namespace %s", yataiApiTokenSecretName, opt.BentoRequest.Namespace)
		} else {
			opt.EventRecorder.Eventf(opt.BentoRequest, corev1.EventTypeNormal, "CreateImageBuilderPod", "Secret %s is found in namespace %s, so updating it", yataiApiTokenSecretName, opt.BentoRequest.Namespace)
			_, err = kubeCli.CoreV1().Secrets(opt.BentoRequest.Namespace).Update(ctx, yataiApiTokenSecret, metav1.UpdateOptions{})
			if err != nil {
				err = errors.Wrapf(err, "failed to update secret %s", yataiApiTokenSecretName)
				return
			}
			opt.EventRecorder.Eventf(opt.BentoRequest, corev1.EventTypeNormal, "CreateImageBuilderPod", "Secret %s is updated in namespace %s", yataiApiTokenSecretName, opt.BentoRequest.Namespace)
		}
	}

	internalImages := commonconfig.GetInternalImages()
	logrus.Infof("Image builder is using the images %v", *internalImages)

	bentoDownloadCommandTemplate, err := template.New("downloadCommand").Parse(`
set -e

mkdir -p /workspace/buildcontext
url="{{.BentoDownloadURL}}"
echo "Downloading bento {{.BentoRepositoryName}}:{{.BentoVersion}} tar file from ${url} to /tmp/downloaded.tar..."
curl --fail -H "{{.BentoDownloadHeader}}" ${url} --output /tmp/downloaded.tar --progress-bar
cd /workspace/buildcontext
echo "Extracting bento tar file..."
tar -xvf /tmp/downloaded.tar
echo "Removing bento tar file..."
rm /tmp/downloaded.tar
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
	})
	if err != nil {
		err = errors.Wrap(err, "failed to execute download command template")
		return
	}

	bentoDownloadCommand := bentoDownloadCommandBuffer.String()

	downloaderContainerResources := corev1.ResourceRequirements{
		Limits: corev1.ResourceList{
			corev1.ResourceCPU:    resource.MustParse("1000m"),
			corev1.ResourceMemory: resource.MustParse("1000Mi"),
		},
		Requests: corev1.ResourceList{
			corev1.ResourceCPU:    resource.MustParse("100m"),
			corev1.ResourceMemory: resource.MustParse("100Mi"),
		},
	}

	downloaderContainerEnvFrom := opt.BentoRequest.Spec.DownloaderContainerEnvFrom

	if yataiApiTokenSecretName != "" {
		downloaderContainerEnvFrom = append(downloaderContainerEnvFrom, corev1.EnvFromSource{
				SecretRef: &corev1.SecretEnvSource{
					LocalObjectReference: corev1.LocalObjectReference{
						Name: yataiApiTokenSecretName,
					},
				},
		})
	}

	initContainers := []corev1.Container{
		{
			Name:  "bento-downloader",
			Image: internalImages.Curl,
			Command: []string{
				"sh",
				"-c",
				bentoDownloadCommand,
			},
			VolumeMounts: volumeMounts,
			Resources: downloaderContainerResources,
			EnvFrom: downloaderContainerEnvFrom,
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

			var yataiClient *yataiclient.YataiClient
			var yataiConf *commonconfig.YataiConfig

			yataiClient, yataiConf, err = getYataiClient(ctx)
			if err != nil {
				err = errors.Wrap(err, "get yatai client")
				return
			}

			if yataiConf.Endpoint == "" {
				err = errors.New("yatai endpoint is empty")
				return
			}
			
			var model_ *schemasv1.ModelFullSchema
			opt.EventRecorder.Eventf(opt.BentoRequest, corev1.EventTypeNormal, "CreateImageBuilderPod", "Getting model %s from yatai service", model.Tag)
			model_, err = yataiClient.GetModel(ctx, modelRepositoryName, modelVersion)
			if err != nil {
				err = errors.Wrap(err, "get model")
				return
			}
			opt.EventRecorder.Eventf(opt.BentoRequest, corev1.EventTypeNormal, "CreateImageBuilderPod", "Model %s is got from yatai service", model.Tag)

			if model_.TransmissionStrategy == modelschemas.TransmissionStrategyPresignedURL {
				var model__ *schemasv1.ModelSchema
				opt.EventRecorder.Eventf(opt.BentoRequest, corev1.EventTypeNormal, "CreateImageBuilderPod", "Getting presigned url for model %s from yatai service", model.Tag)
				model__, err = yataiClient.PresignModelDownloadURL(ctx, modelRepositoryName, modelVersion)
				if err != nil {
					err = errors.Wrap(err, "presign model download url")
					return
				}
				opt.EventRecorder.Eventf(opt.BentoRequest, corev1.EventTypeNormal, "CreateImageBuilderPod", "Presigned url for model %s is got from yatai service", model.Tag)
				modelDownloadURL = model__.PresignedDownloadUrl
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
curl --fail -H "{{.ModelDownloadHeader}}" ${url} --output /tmp/downloaded.tar --progress-bar
cd {{.ModelDirPath}}
echo "Extracting model tar file..."
tar -xvf /tmp/downloaded.tar
echo -n '{{.ModelVersion}}' > {{.ModelRepositoryDirPath}}/latest
echo "Removing model tar file..."
rm /tmp/downloaded.tar
echo "Done"
`)).Execute(&modelDownloadCommandOutput, map[string]interface{}{
			"ModelDirPath":           modelDirPath,
			"ModelDownloadURL":       modelDownloadURL,
			"ModelDownloadHeader":    modelDownloadHeader,
			"ModelRepositoryDirPath": modelRepositoryDirPath,
			"ModelRepositoryName":    modelRepositoryName,
			"ModelVersion":           modelVersion,
		})
		if err != nil {
			err = errors.Wrap(err, "failed to generate download command")
			return
		}
		modelDownloadCommand := modelDownloadCommandOutput.String()
		initContainers = append(initContainers, corev1.Container{
			Name:  fmt.Sprintf("model-downloader-%d", idx),
			Image: internalImages.Curl,
			Command: []string{
				"sh",
				"-c",
				modelDownloadCommand,
			},
			VolumeMounts: volumeMounts,
			Resources: downloaderContainerResources,
			EnvFrom: downloaderContainerEnvFrom,
		})
	}

	var extraPodMetadata *resourcesv1alpha1.ExtraPodMetadata
	var extraPodSpec *resourcesv1alpha1.ExtraPodSpec
	var extraContainerEnv []corev1.EnvVar
	var buildArgs []string
	var builderArgs []string

	configNamespace, err := commonconfig.GetYataiImageBuilderNamespace(ctx, kubeCli)
	if err != nil {
		err = errors.Wrap(err, "failed to get Yatai image builder namespace")
		return
	}

	configCmName := "yatai-image-builder-config"
	opt.EventRecorder.Eventf(opt.BentoRequest, corev1.EventTypeNormal, "CreateImageBuilderPod", "Getting configmap %s from namespace %s", configCmName, configNamespace)
	configCm, err := kubeCli.CoreV1().ConfigMaps(configNamespace).Get(ctx, configCmName, metav1.GetOptions{})
	configCmIsNotFound := apierrors.IsNotFound(err)
	if err != nil && !configCmIsNotFound {
		return
	}

	if !configCmIsNotFound {
		opt.EventRecorder.Eventf(opt.BentoRequest, corev1.EventTypeNormal, "CreateImageBuilderPod", "Configmap %s is got from namespace %s", configCmName, configNamespace)
		extraPodMetadata = &resourcesv1alpha1.ExtraPodMetadata{}

		if val, ok := configCm.Data["extra_pod_metadata"]; ok {
			err = json.Unmarshal([]byte(val), extraPodMetadata)
			if err != nil {
				err = errors.Wrapf(err, "failed to unmarshal extra_pod_metadata, please check the configmap %s in namespace %s", configCmName, configNamespace)
				return
			}
		}

		extraPodSpec = &resourcesv1alpha1.ExtraPodSpec{}

		if val, ok := configCm.Data["extra_pod_spec"]; ok {
			err = json.Unmarshal([]byte(val), extraPodSpec)
			if err != nil {
				err = errors.Wrapf(err, "failed to unmarshal extra_pod_spec, please check the configmap %s in namespace %s", configCmName, configNamespace)
				return
			}
		}

		extraContainerEnv = []corev1.EnvVar{}

		if val, ok := configCm.Data["extra_container_env"]; ok {
			err = json.Unmarshal([]byte(val), &extraContainerEnv)
			if err != nil {
				err = errors.Wrapf(err, "failed to unmarshal extra_container_env, please check the configmap %s in namespace %s", configCmName, configNamespace)
				return
			}
		}

		buildArgs = []string{}

		if val, ok := configCm.Data["build_args"]; ok {
			err = json.Unmarshal([]byte(val), &buildArgs)
			if err != nil {
				err = errors.Wrapf(err, "failed to unmarshal build_args, please check the configmap %s in namespace %s", configCmName, configNamespace)
				return
			}
		}

		builderArgs = []string{}
		if val, ok := configCm.Data["builder_args"]; ok {
			err = json.Unmarshal([]byte(val), &builderArgs)
			if err != nil {
				err = errors.Wrapf(err, "failed to unmarshal builder_args, please check the configmap %s in namespace %s", configCmName, configNamespace)
				return
			}
		}
		logrus.Info("passed in builder args: ", builderArgs)
	} else {
		opt.EventRecorder.Eventf(opt.BentoRequest, corev1.EventTypeNormal, "CreateImageBuilderPod", "Configmap %s is not found in namespace %s", configCmName, configNamespace)
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

	var command []string
	args := []string{
		"--context=/workspace/buildcontext",
		"--verbosity=info",
		fmt.Sprintf("--dockerfile=%s", dockerFilePath),
		fmt.Sprintf("--insecure=%v", dockerRegistryInsecure),
		fmt.Sprintf("--destination=%s", inClusterImageName),
	}

	// add build args to pass via --build-arg
	for _, buildArg := range buildArgs {
		args = append(args, fmt.Sprintf("--build-arg=%s", buildArg))
	}
	// add other arguments to builder
	args = append(args, builderArgs...)
	logrus.Info("yatai-image-builder args: ", args)

	// nolint: gosec
	buildArgsSecretName := "yatai-image-builder-build-args"
	opt.EventRecorder.Eventf(opt.BentoRequest, corev1.EventTypeNormal, "CreateImageBuilderPod", "Getting secret %s from namespace %s", buildArgsSecretName, configNamespace)
	buildArgsSecret, err := kubeCli.CoreV1().Secrets(configNamespace).Get(ctx, buildArgsSecretName, metav1.GetOptions{})
	buildArgsSecretIsNotFound := apierrors.IsNotFound(err)
	if err != nil && !buildArgsSecretIsNotFound {
		return
	}

	if !buildArgsSecretIsNotFound {
		opt.EventRecorder.Eventf(opt.BentoRequest, corev1.EventTypeNormal, "CreateImageBuilderPod", "Secret %s is got from namespace %s", buildArgsSecretName, configNamespace)
		if configNamespace != opt.BentoRequest.Namespace {
			opt.EventRecorder.Eventf(opt.BentoRequest, corev1.EventTypeNormal, "CreateImageBuilderPod", "Secret %s is in namespace %s, but BentoRequest is in namespace %s, so we need to copy the secret to BentoRequest namespace", buildArgsSecretName, configNamespace, opt.BentoRequest.Namespace)
			opt.EventRecorder.Eventf(opt.BentoRequest, corev1.EventTypeNormal, "CreateImageBuilderPod", "Getting secret %s in namespace %s", buildArgsSecretName, opt.BentoRequest.Namespace)
			_, err = kubeCli.CoreV1().Secrets(opt.BentoRequest.Namespace).Get(ctx, buildArgsSecretName, metav1.GetOptions{})
			localBuildArgsSecretIsNotFound := apierrors.IsNotFound(err)
			if err != nil && !localBuildArgsSecretIsNotFound {
				err = errors.Wrapf(err, "failed to get secret %s from namespace %s", buildArgsSecretName, opt.BentoRequest.Namespace)
				return
			}
			if localBuildArgsSecretIsNotFound {
				opt.EventRecorder.Eventf(opt.BentoRequest, corev1.EventTypeNormal, "CreateImageBuilderPod", "Copying secret %s from namespace %s to namespace %s", buildArgsSecretName, configNamespace, opt.BentoRequest.Namespace)
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
				opt.EventRecorder.Eventf(opt.BentoRequest, corev1.EventTypeNormal, "CreateImageBuilderPod", "Secret %s is already in namespace %s", buildArgsSecretName, opt.BentoRequest.Namespace)
				opt.EventRecorder.Eventf(opt.BentoRequest, corev1.EventTypeNormal, "CreateImageBuilderPod", "Updating secret %s in namespace %s", buildArgsSecretName, opt.BentoRequest.Namespace)
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

			args = append(args, fmt.Sprintf("--build-arg=%s=$(%s)", key, envName))
		}
	} else {
		opt.EventRecorder.Eventf(opt.BentoRequest, corev1.EventTypeNormal, "CreateImageBuilderPod", "Secret %s is not found in namespace %s", buildArgsSecretName, configNamespace)
	}

	builderImage := internalImages.Kaniko

	podsCli := kubeCli.CoreV1().Pods(opt.BentoRequest.Namespace)

	pod = &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Name:      kubeName,
			Namespace: opt.BentoRequest.Namespace,
			Labels:    kubeLabels,
		},
		Spec: corev1.PodSpec{
			RestartPolicy:  corev1.RestartPolicyNever,
			Volumes:        volumes,
			InitContainers: initContainers,
			Containers: []corev1.Container{
				{
					Name:            "builder",
					Image:           builderImage,
					ImagePullPolicy: corev1.PullAlways,
					Command:         command,
					Args:            args,
					VolumeMounts:    volumeMounts,
					Env:             envs,
					TTY:             true,
					Stdin:           true,
					Resources: opt.BentoRequest.Spec.ImageBuilderContainerResources,
				},
			},
		},
	}

	if extraPodMetadata != nil {
		for k, v := range extraPodMetadata.Annotations {
			pod.Annotations[k] = v
		}

		for k, v := range extraPodMetadata.Labels {
			pod.Labels[k] = v
		}
	}

	for k, v := range opt.BentoRequest.Spec.ImageBuilderExtraPodMetadata.Annotations {
		pod.Annotations[k] = v
	}

	for k, v := range opt.BentoRequest.Spec.ImageBuilderExtraPodMetadata.Labels {
		pod.Labels[k] = v
	}

	if extraPodSpec != nil {
		pod.Spec.SchedulerName = extraPodSpec.SchedulerName
		pod.Spec.NodeSelector = extraPodSpec.NodeSelector
		pod.Spec.Affinity = extraPodSpec.Affinity
		pod.Spec.Tolerations = extraPodSpec.Tolerations
		pod.Spec.TopologySpreadConstraints = extraPodSpec.TopologySpreadConstraints
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

	if extraContainerEnv != nil {
		for i, c := range pod.Spec.InitContainers {
			env := c.Env
			env = append(env, extraContainerEnv...)
			pod.Spec.InitContainers[i].Env = env
		}
		for i, c := range pod.Spec.Containers {
			env := c.Env
			env = append(env, extraContainerEnv...)
			pod.Spec.Containers[i].Env = env
		}
	}

	opt.EventRecorder.Eventf(opt.BentoRequest, corev1.EventTypeNormal, "CreateImageBuilderPod", "Creating image builder pod %s", kubeName)
	oldPod, err := podsCli.Get(ctx, kubeName, metav1.GetOptions{})
	isNotFound := apierrors.IsNotFound(err)
	if !isNotFound && err != nil {
		return
	}
	if isNotFound {
		_, err = podsCli.Create(ctx, pod, metav1.CreateOptions{})
		isExists := apierrors.IsAlreadyExists(err)
		if err != nil && !isExists {
			err = errors.Wrapf(err, "failed to create pod %s", kubeName)
			return
		}
		err = nil
	} else {
		var patchResult *patch.PatchResult
		patchResult, err = patch.DefaultPatchMaker.Calculate(oldPod, pod)
		if err != nil {
			err = errors.Wrapf(err, "failed to calculate patch for pod %s", kubeName)
			return
		}

		if !patchResult.IsEmpty() || (oldPod.Status.Phase == corev1.PodFailed && opt.RecreateIfFailed) {
			err = podsCli.Delete(ctx, kubeName, metav1.DeleteOptions{})
			if err != nil {
				err = errors.Wrapf(err, "failed to delete pod %s", kubeName)
				return
			}
			_, err = podsCli.Create(ctx, pod, metav1.CreateOptions{})
			isExists := apierrors.IsAlreadyExists(err)
			if err != nil && !isExists {
				err = errors.Wrapf(err, "failed to create pod %s", kubeName)
				return
			}
			err = nil
		} else {
			pod = oldPod
		}
	}
	opt.EventRecorder.Eventf(opt.BentoRequest, corev1.EventTypeNormal, "CreatedImageBuilderPod", "Created image builder pod %s", kubeName)

	return
}
