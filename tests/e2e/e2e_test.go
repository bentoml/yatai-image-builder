//nolint:wrapcheck,gosec
package e2e

import (
	"context"
	"fmt"
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
		By("Showing image builder pod events")
		cmd := exec.Command("kubectl", "-n", "yatai", "describe", "pod", "-l", fmt.Sprintf("%s=true", commonconsts.KubeLabelIsBentoImageBuilder))
		logs, _ := utils.Run(cmd)
		fmt.Println(string(logs))
		By("Showing image builder pod logs")
		cmd = exec.Command("kubectl", "-n", "yatai", "logs", "--tail", "200", "-l", fmt.Sprintf("%s=true", commonconsts.KubeLabelIsBentoImageBuilder))
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
			EventuallyWithOffset(1, func() error {
				cmd := exec.Command("kubectl", "apply", "-f", "tests/e2e/example.yaml")
				_, err := utils.Run(cmd)
				return err
			}, time.Minute, time.Second).Should(Succeed())

			By("Sleeping for 5 seconds")
			time.Sleep(5 * time.Second)

			restConf := config.GetConfigOrDie()
			cliset, err := kubernetes.NewForConfig(restConf)
			Expect(err).To(BeNil(), "failed to create kubernetes clientset")

			By("Checking the generated image builder pod")
			EventuallyWithOffset(1, func() error {
				pods, err := cliset.CoreV1().Pods("yatai").List(context.Background(), metav1.ListOptions{
					LabelSelector: fmt.Sprintf("%s=true", commonconsts.KubeLabelIsBentoImageBuilder),
				})
				if err != nil {
					return err
				}
				if len(pods.Items) != 1 {
					return fmt.Errorf("expected 1 pod, got %d", len(pods.Items))
				}
				pod := pods.Items[0]
				if pod.Status.Phase == corev1.PodFailed {
					return fmt.Errorf("pod failed: %s", pod.Status.Message)
				}
				if pod.Status.Phase != corev1.PodSucceeded {
					return fmt.Errorf("pod not finished yet")
				}
				return nil
			}, 10*time.Minute, time.Second).Should(Succeed())

			bentorequestcli, err := resourcesclient.NewForConfig(restConf)
			Expect(err).To(BeNil(), "failed to create bentorequest clientset")

			By("Sleeping for 5 seconds")
			time.Sleep(5 * time.Second)

			By("Checking the generated Bento CR")
			EventuallyWithOffset(1, func() error {
				ctx := context.Background()

				logrus.Infof("Getting BentoRequest CR %s", "test-bento")
				bentoRequest, err := bentorequestcli.BentoRequests("yatai").Get(ctx, "test-bento", metav1.GetOptions{})
				if err != nil {
					return err
				}

				logrus.Infof("Getting Bento CR %s", "test-bento")
				bento, err := bentorequestcli.Bentoes("yatai").Get(ctx, "test-bento", metav1.GetOptions{})
				if err != nil {
					return err
				}

				Expect(bentoRequest.Spec.BentoTag).To(Equal(bento.Spec.Tag), "BentoRequest and Bento tag should match")
				Expect(bento.Spec.Image).To(Not(BeEmpty()), "Bento CR image should not be empty")
				return nil
			}).Should(Succeed())
		})
	})
})
