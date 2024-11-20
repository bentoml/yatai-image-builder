//nolint:wrapcheck,gosec
package e2e

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"time"

	"github.com/sirupsen/logrus"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
	"sigs.k8s.io/controller-runtime/pkg/client/config"

	commonconsts "github.com/bentoml/yatai-common/consts"

	resourcesclient "github.com/bentoml/yatai-image-builder/generated/resources/clientset/versioned/typed/resources/v1alpha1"

	//nolint:golint
	//nolint:revive
	. "github.com/onsi/ginkgo/v2"

	//nolint:golint
	//nolint:revive
	. "github.com/onsi/gomega"

	"github.com/bentoml/yatai-image-builder/tests/utils"
)

var _ = Describe("yatai-image-builder", Ordered, func() {
	AfterAll(func() {
		if os.Getenv("DEBUG") == "true" {
			return
		}
		By("Showing image builder pod events")
		cmd := exec.Command("kubectl", "-n", "yatai", "describe", "pod", "-l", commonconsts.KubeLabelIsBentoImageBuilder+"=true")
		logs, _ := utils.Run(cmd)
		fmt.Println(string(logs))
		By("Showing image builder pod bento-downloader container logs")
		cmd = exec.Command("kubectl", "-n", "yatai", "logs", "-c", "bento-downloader", "--tail", "200", "-l", commonconsts.KubeLabelIsBentoImageBuilder+"=true")
		logs, _ = utils.Run(cmd)
		fmt.Println(string(logs))
		By("Showing image builder pod builder container logs")
		cmd = exec.Command("kubectl", "-n", "yatai", "logs", "-c", "builder", "--tail", "200", "-l", commonconsts.KubeLabelIsBentoImageBuilder+"=true")
		logs, _ = utils.Run(cmd)
		fmt.Println(string(logs))
		By("Showing yatai-image-builder events")
		cmd = exec.Command("kubectl", "-n", "yatai-image-builder", "describe", "pod", "-l", "app.kubernetes.io/name=yatai-image-builder")
		logs, _ = utils.Run(cmd)
		fmt.Println(string(logs))
		By("Showing yatai-image-builder logs")
		cmd = exec.Command("kubectl", "-n", "yatai-image-builder", "logs", "--tail", "200", "-l", "app.kubernetes.io/name=yatai-image-builder")
		logs, _ = utils.Run(cmd)
		fmt.Println(string(logs))
		By("Cleaning up BentoRequest resources")
		cmd = exec.Command("kubectl", "delete", "-f", "tests/e2e/example.yaml")
		_, _ = utils.Run(cmd)
	})

	Context("BentoRequest Operator", func() {
		It("Should run successfully", func() {
			By("Creating a BentoRequest CR")
			cmd := exec.Command("kubectl", "apply", "-f", "tests/e2e/example.yaml")
			out, err := utils.Run(cmd)
			Expect(err).To(BeNil(), "Failed to create BentoRequest CR: %s", string(out))

			By("Sleeping for 5 seconds")
			time.Sleep(5 * time.Second)

			restConf := config.GetConfigOrDie()
			cliset, err := kubernetes.NewForConfig(restConf)
			Expect(err).To(BeNil(), "failed to create kubernetes clientset")

			By("Checking the generated image builder pod")
			EventuallyWithOffset(1, func() error {
				pods, err := cliset.CoreV1().Pods("yatai").List(context.Background(), metav1.ListOptions{
					LabelSelector: commonconsts.KubeLabelIsBentoImageBuilder + "=true",
				})
				if err != nil {
					return err
				}
				if len(pods.Items) != 2 {
					return fmt.Errorf("expected 2 pod, got %d", len(pods.Items))
				}
				pod := pods.Items[0]
				if pod.Status.Phase == corev1.PodFailed {
					Fail(fmt.Sprintf("pod %s failed", pod.Name))
				}
				if pod.Status.Phase != corev1.PodSucceeded {
					return errors.New("pod not finished yet")
				}
				return nil
			}, 10*time.Minute, time.Second).Should(Succeed())

			bentorequestcli, err := resourcesclient.NewForConfig(restConf)
			Expect(err).To(BeNil(), "failed to create bentorequest clientset")

			By("Checking the generated Bento CR")
			EventuallyWithOffset(1, func() error {
				ctx := context.Background()

				logrus.Infof("Getting BentoRequest CR %s", "test-bento")
				bentoRequest, err := bentorequestcli.BentoRequests("yatai").Get(ctx, "test-bento", metav1.GetOptions{})
				Expect(err).To(BeNil(), "failed to get BentoRequest CR test-bento")

				logrus.Infof("Getting Bento CR %s", "test-bento")
				bento, err := bentorequestcli.Bentoes("yatai").Get(ctx, "test-bento", metav1.GetOptions{})
				if err != nil {
					return err
				}

				Expect(bentoRequest.Spec.BentoTag).To(Equal(bento.Spec.Tag), "bentoRequest and bento tag should match")
				Expect(bento.Spec.Image).To(Not(BeEmpty()), "bento CR image should not be empty")

				logrus.Infof("Getting BentoRequest CR %s", "test-bento1")

				bentoRequest1, err := bentorequestcli.BentoRequests("yatai").Get(ctx, "test-bento1", metav1.GetOptions{})
				Expect(err).To(BeNil(), "failed to get BentoRequest CR test-bento1")

				logrus.Infof("Getting Bento CR %s", "test-bento1")
				bento1, err := bentorequestcli.Bentoes("yatai").Get(ctx, "test-bento1", metav1.GetOptions{})
				if err != nil {
					return err
				}

				Expect(bentoRequest1.Spec.BentoTag).To(Equal(bento1.Spec.Tag), "bentoRequest1 and bento1 tag should match")
				Expect(bento1.Spec.Image).To(Not(BeEmpty()), "bento1 CR image should not be empty")
				Expect(bentoRequest1.Spec.Image).To(Equal(bento1.Spec.Image), "bentoRequest1 and bento1 image should match")
				return nil
			}, time.Minute, time.Second).Should(Succeed())
		})
	})
})
