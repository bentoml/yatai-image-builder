package resources

import (
	"context"
	"testing"

	commonconfig "github.com/bentoml/yatai-common/config"
)

func TestSharedRegistryCredentialTenantBoundary(t *testing.T) {
	t.Setenv("DISABLE_TENANT_DEFAULT_REGISTRY_CREDENTIAL", "true")
	if shouldCopyDefaultRegistryCredential("org-customer--gcp-us-central-1") {
		t.Fatal("shared registry credential must not be copied into a tenant namespace")
	}
	if !shouldCopyDefaultRegistryCredential("yatai") {
		t.Fatal("platform builder namespace must retain its registry credential")
	}
	secret, err := (*BentoRequestReconciler)(nil).makeSureDockerConfigJSONSecret(
		context.Background(), "org-customer--gcp-us-central-1", &commonconfig.DockerRegistryConfig{Username: "_json_key"},
	)
	if err != nil || secret != nil {
		t.Fatalf("tenant registry Secret writer ran: secret=%v err=%v", secret, err)
	}
}

func TestSharedRegistryCredentialOptIn(t *testing.T) {
	t.Setenv("DISABLE_TENANT_DEFAULT_REGISTRY_CREDENTIAL", "false")
	if !shouldCopyDefaultRegistryCredential("org-customer--gcp-us-central-1") {
		t.Fatal("default behavior changed without the rollout flag")
	}
}
