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

package main

import (
	"context"
	"flag"
	"net/url"
	"os"

	// Import all Kubernetes client auth plugins (e.g. Azure, GCP, OIDC, etc.)
	// to ensure that exec-entrypoint and run can make use of them.
	"emperror.dev/errors"
	corev1 "k8s.io/api/core/v1"
	_ "k8s.io/client-go/plugin/pkg/client/auth"

	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	utilruntime "k8s.io/apimachinery/pkg/util/runtime"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/healthz"
	"sigs.k8s.io/controller-runtime/pkg/log/zap"

	"github.com/minio/minio-go/v7"
	"github.com/minio/minio-go/v7/pkg/credentials"

	commonconfig "github.com/bentoml/yatai-common/config"
	"github.com/bentoml/yatai-common/conncheck"
	resourcesv1alpha1 "github.com/bentoml/yatai-image-builder/apis/resources/v1alpha1"
	resourcescontrollers "github.com/bentoml/yatai-image-builder/controllers/resources"
	//+kubebuilder:scaffold:imports
)

var (
	scheme   = runtime.NewScheme()
	setupLog = ctrl.Log.WithName("setup")
)

func init() {
	utilruntime.Must(clientgoscheme.AddToScheme(scheme))

	utilruntime.Must(resourcesv1alpha1.AddToScheme(scheme))
	//+kubebuilder:scaffold:scheme
}

func main() {
	var metricsAddr string
	var enableLeaderElection bool
	var probeAddr string
	var skipCheck bool
	flag.StringVar(&metricsAddr, "metrics-bind-address", ":8080", "The address the metric endpoint binds to.")
	flag.StringVar(&probeAddr, "health-probe-bind-address", ":8081", "The address the probe endpoint binds to.")
	flag.BoolVar(&enableLeaderElection, "leader-elect", false,
		"Enable leader election for controller manager. "+
			"Enabling this will ensure there is only one active controller manager.")
	flag.BoolVar(&skipCheck, "skip-check", false, "Skip check")
	opts := zap.Options{
		Development: true,
	}
	opts.BindFlags(flag.CommandLine)
	flag.Parse()

	ctrl.SetLogger(zap.New(zap.UseFlagOptions(&opts)))

	mgr, err := ctrl.NewManager(ctrl.GetConfigOrDie(), ctrl.Options{
		Scheme:                 scheme,
		HealthProbeBindAddress: probeAddr,
		LeaderElection:         enableLeaderElection,
		LeaderElectionID:       "58b75536.yatai.ai",
		// LeaderElectionReleaseOnCancel defines if the leader should step down voluntarily
		// when the Manager ends. This requires the binary to immediately end when the
		// Manager is stopped, otherwise, this setting is unsafe. Setting this significantly
		// speeds up voluntary leader transitions as the new leader don't have to wait
		// LeaseDuration time first.
		//
		// In the default scaffold provided, the program ends immediately after
		// the manager stops, so would be fine to enable this option. However,
		// if you are doing or is intended to do any operation such as perform cleanups
		// after the manager stops then its usage might be unsafe.
		// LeaderElectionReleaseOnCancel: true,
	})
	if err != nil {
		setupLog.Error(err, "unable to start manager")
		os.Exit(1)
	}

	if err = (&resourcescontrollers.BentoRequestReconciler{
		Client:   mgr.GetClient(),
		Scheme:   mgr.GetScheme(),
		Recorder: mgr.GetEventRecorderFor("yatai-image-builder"),
	}).SetupWithManager(mgr); err != nil {
		setupLog.Error(err, "unable to create controller", "controller", "BentoRequest")
		os.Exit(1)
	}
	//+kubebuilder:scaffold:builder

	if !skipCheck {
		if err := verifyConfigurations(context.Background(), mgr.GetClient()); err != nil {
			setupLog.Error(err, "failed to verify configurations")
			os.Exit(1)
			return
		}
	}

	if err := mgr.AddHealthzCheck("healthz", healthz.Ping); err != nil {
		setupLog.Error(err, "unable to set up health check")
		os.Exit(1)
	}
	if err := mgr.AddReadyzCheck("readyz", healthz.Ping); err != nil {
		setupLog.Error(err, "unable to set up ready check")
		os.Exit(1)
	}

	setupLog.Info("starting manager")
	if err := mgr.Start(ctrl.SetupSignalHandler()); err != nil {
		setupLog.Error(err, "problem running manager")
		os.Exit(1)
	}
}

func verifyConfigurations(ctx context.Context, c client.Client) error {
	// test s3 connection
	rawURL := resourcescontrollers.GetContainerImageS3EndpointURL()
	parsedURL, err := url.Parse(rawURL)
	if err != nil {
		return errors.Wrap(err, "failed to parse S3 endpoint URL")
	}
	containerImageS3EndpointURL := parsedURL.Host
	containerImageS3Bucket := resourcescontrollers.GetContainerImageS3Bucket()
	containerImageS3AccessKeyID := resourcescontrollers.GetContainerImageS3AccessKeyID()
	containerImageS3SecretAccessKey := resourcescontrollers.GetContainerImageS3SecretAccessKey()
	minioClient, err := minio.New(containerImageS3EndpointURL, &minio.Options{
		Creds: credentials.NewStaticV4(containerImageS3AccessKeyID, containerImageS3SecretAccessKey, ""),
	})
	if err != nil {
		return errors.Wrap(err, "failed to create minio client")
	}
	s3Probe := conncheck.NewS3Probe(minioClient)
	err = s3Probe.Test(ctx, "imagebuilder", containerImageS3Bucket)
	if err != nil {
		return errors.Wrap(err, "failed to test s3 connection")
	}

	// test docker registry connection
	dockerRegistryConfig, err := commonconfig.GetDockerRegistryConfig(ctx, func(ctx context.Context, namespace, name string) (*corev1.Secret, error) {
		secret := &corev1.Secret{}
		err := c.Get(ctx, types.NamespacedName{
			Namespace: namespace,
			Name:      name,
		}, secret)
		return secret, errors.Wrap(err, "get secret")
	})
	if err != nil {
		return errors.Wrap(err, "failed to get docker registry config")
	}
	registryProbe := conncheck.NewRegistryProbe(conncheck.RegistryConfig{
		Endpoint: dockerRegistryConfig.Server,
		Username: dockerRegistryConfig.Username,
		Password: dockerRegistryConfig.Password,
	})
	err = registryProbe.Test(ctx)
	if err != nil {
		return errors.Wrap(err, "failed to test docker registry connection")
	}
	return nil
}
