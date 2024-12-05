module github.com/bentoml/yatai-image-builder

go 1.23

toolchain go1.23.1

require (
	github.com/apparentlymart/go-shquot v0.0.1
	github.com/asottile/dockerfile v2.2.0+incompatible
	github.com/aws/aws-sdk-go v1.55.5
	github.com/aws/aws-sdk-go-v2 v1.24.1
	github.com/aws/aws-sdk-go-v2/credentials v1.16.16
	github.com/aws/aws-sdk-go-v2/service/s3 v1.48.1
	github.com/aws/smithy-go v1.19.0
	github.com/bentoml/yatai-common v0.0.0-20241107031151-f7d5e9220f8a
	github.com/bentoml/yatai-schemas v0.0.0-20231012015630-b7ce9413265c
	github.com/containerd/containerd/api v1.8.0
	github.com/containerd/containerd/v2 v2.0.0
	github.com/containerd/continuity v0.4.4
	github.com/containerd/errdefs v1.0.0
	github.com/containerd/log v0.1.0
	github.com/containerd/stargz-snapshotter v0.16.1
	github.com/containerd/stargz-snapshotter/estargz v0.16.1
	github.com/coreos/go-systemd/v22 v22.5.0
	github.com/docker/docker v27.3.1+incompatible
	github.com/hanwen/go-fuse/v2 v2.6.3
	github.com/huandu/xstrings v1.3.2
	github.com/iancoleman/strcase v0.2.0
	github.com/jessevdk/go-flags v1.6.1
	github.com/klauspost/compress v1.17.11
	github.com/mitchellh/hashstructure/v2 v2.0.2
	github.com/moby/sys/mountinfo v0.7.2
	github.com/onsi/ginkgo/v2 v2.19.0
	github.com/onsi/gomega v1.33.1
	github.com/opencontainers/go-digest v1.0.0
	github.com/opencontainers/image-spec v1.1.0
	github.com/pkg/errors v0.9.1
	github.com/rs/xid v1.6.0
	github.com/sergeymakinen/go-quote v1.1.0
	github.com/sirupsen/logrus v1.9.3
	github.com/tidwall/btree v1.7.0
	github.com/zeebo/blake3 v0.2.4
	golang.org/x/sync v0.9.0
	google.golang.org/grpc v1.68.0
	k8s.io/api v0.31.2
	k8s.io/apimachinery v0.31.2
	k8s.io/client-go v0.31.2
	k8s.io/code-generator v0.31.2
	sigs.k8s.io/controller-runtime v0.19.1
)

require (
	github.com/AdaLogics/go-fuzz-headers v0.0.0-20240806141605-e8a1dd7889d6 // indirect
	github.com/Microsoft/go-winio v0.6.2 // indirect
	github.com/Microsoft/hcsshim v0.12.9 // indirect
	github.com/aws/aws-sdk-go-v2/aws/protocol/eventstream v1.5.4 // indirect
	github.com/aws/aws-sdk-go-v2/internal/configsources v1.2.10 // indirect
	github.com/aws/aws-sdk-go-v2/internal/endpoints/v2 v2.5.10 // indirect
	github.com/aws/aws-sdk-go-v2/internal/v4a v1.2.10 // indirect
	github.com/aws/aws-sdk-go-v2/service/internal/accept-encoding v1.10.4 // indirect
	github.com/aws/aws-sdk-go-v2/service/internal/checksum v1.2.10 // indirect
	github.com/aws/aws-sdk-go-v2/service/internal/presigned-url v1.10.10 // indirect
	github.com/aws/aws-sdk-go-v2/service/internal/s3shared v1.16.10 // indirect
	github.com/bentoml/yatai-deployment v1.1.10 // indirect
	github.com/containerd/cgroups/v3 v3.0.3 // indirect
	github.com/containerd/errdefs/pkg v0.3.0 // indirect
	github.com/containerd/platforms v1.0.0-rc.0 // indirect
	github.com/containerd/ttrpc v1.2.6 // indirect
	github.com/containerd/typeurl/v2 v2.2.2 // indirect
	github.com/distribution/reference v0.6.0 // indirect
	github.com/docker/distribution v2.8.2+incompatible // indirect
	github.com/docker/go-connections v0.5.0 // indirect
	github.com/docker/go-metrics v0.0.1 // indirect
	github.com/docker/go-units v0.5.0 // indirect
	github.com/docker/libtrust v0.0.0-20160708172513-aabc10ec26b7 // indirect
	github.com/felixge/httpsnoop v1.0.4 // indirect
	github.com/fxamacker/cbor/v2 v2.7.0 // indirect
	github.com/go-logr/stdr v1.2.2 // indirect
	github.com/go-task/slim-sprig/v3 v3.0.0 // indirect
	github.com/google/gnostic-models v0.6.8 // indirect
	github.com/google/pprof v0.0.0-20240727154555-813a5fbdbec8 // indirect
	github.com/hashicorp/errwrap v1.1.0 // indirect
	github.com/hashicorp/go-cleanhttp v0.5.2 // indirect
	github.com/hashicorp/go-multierror v1.1.1 // indirect
	github.com/hashicorp/go-retryablehttp v0.7.7 // indirect
	github.com/jmespath/go-jmespath v0.4.0 // indirect
	github.com/klauspost/cpuid/v2 v2.2.6 // indirect
	github.com/moby/buildkit v0.17.0 // indirect
	github.com/moby/docker-image-spec v1.3.1 // indirect
	github.com/moby/locker v1.0.1 // indirect
	github.com/moby/sys/userns v0.1.0 // indirect
	github.com/planetscale/vtprotobuf v0.6.1-0.20240319094008-0393e58bdf10 // indirect
	github.com/ulikunitz/xz v0.5.12 // indirect
	github.com/vbatts/tar-split v0.11.6 // indirect
	github.com/x448/float16 v0.8.4 // indirect
	go.etcd.io/bbolt v1.3.11 // indirect
	go.opencensus.io v0.24.0 // indirect
	go.opentelemetry.io/contrib/instrumentation/net/http/otelhttp v0.56.0 // indirect
	go.opentelemetry.io/otel v1.31.0 // indirect
	go.opentelemetry.io/otel/metric v1.31.0 // indirect
	go.opentelemetry.io/otel/trace v1.31.0 // indirect
	golang.org/x/exp v0.0.0-20241108190413-2d47ceb2692f // indirect
	google.golang.org/genproto/googleapis/rpc v0.0.0-20241021214115-324edc3d5d38 // indirect
	gopkg.in/evanphx/json-patch.v4 v4.12.0 // indirect
	k8s.io/gengo/v2 v2.0.0-20240826214909-a7b603a56eb7 // indirect
)

require (
	github.com/beorn7/perks v1.0.1 // indirect
	github.com/cespare/xxhash/v2 v2.3.0 // indirect
	github.com/containerd/plugin v1.0.0
	github.com/davecgh/go-spew v1.1.2-0.20180830191138-d8f796af33cc // indirect
	github.com/emicklei/go-restful/v3 v3.12.1 // indirect
	github.com/evanphx/json-patch/v5 v5.9.0 // indirect
	github.com/fsnotify/fsnotify v1.8.0 // indirect
	github.com/go-logr/logr v1.4.2 // indirect
	github.com/go-logr/zapr v1.3.0 // indirect
	github.com/go-openapi/jsonpointer v0.21.0 // indirect
	github.com/go-openapi/jsonreference v0.21.0 // indirect
	github.com/go-openapi/swag v0.23.0 // indirect
	github.com/gogo/protobuf v1.3.2 // indirect
	github.com/golang/groupcache v0.0.0-20210331224755-41bb18bfe9da // indirect
	github.com/golang/protobuf v1.5.4 // indirect
	github.com/google/go-cmp v0.6.0 // indirect
	github.com/google/gofuzz v1.2.0 // indirect
	github.com/google/uuid v1.6.0 // indirect
	github.com/imdario/mergo v0.3.16 // indirect
	github.com/josharian/intern v1.0.0 // indirect
	github.com/json-iterator/go v1.1.12 // indirect
	github.com/mailru/easyjson v0.7.7 // indirect
	github.com/modern-go/concurrent v0.0.0-20180306012644-bacd9c7ef1dd // indirect
	github.com/modern-go/reflect2 v1.0.2 // indirect
	github.com/munnerz/goautoneg v0.0.0-20191010083416-a7dc8b61c822 // indirect
	github.com/prometheus/client_golang v1.20.5 // indirect
	github.com/prometheus/client_model v0.6.1 // indirect
	github.com/prometheus/common v0.60.1 // indirect
	github.com/prometheus/procfs v0.15.1 // indirect
	github.com/prune998/docker-registry-client v0.0.0-20200114164314-f8cd511a014c
	github.com/regclient/regclient v0.7.2
	github.com/spf13/pflag v1.0.5 // indirect
	go.uber.org/multierr v1.11.0 // indirect
	go.uber.org/zap v1.27.0 // indirect
	golang.org/x/mod v0.22.0 // indirect
	golang.org/x/net v0.31.0 // indirect
	golang.org/x/oauth2 v0.24.0 // indirect
	golang.org/x/sys v0.27.0
	golang.org/x/term v0.26.0 // indirect
	golang.org/x/text v0.20.0 // indirect
	golang.org/x/time v0.8.0 // indirect
	golang.org/x/tools v0.27.0 // indirect
	gomodules.xyz/jsonpatch/v2 v2.4.0 // indirect
	google.golang.org/protobuf v1.35.1 // indirect
	gopkg.in/inf.v0 v0.9.1 // indirect
	gopkg.in/yaml.v2 v2.4.0 // indirect
	gopkg.in/yaml.v3 v3.0.1 // indirect
	k8s.io/apiextensions-apiserver v0.31.2 // indirect
	k8s.io/klog/v2 v2.130.1 // indirect
	k8s.io/kube-openapi v0.0.0-20241105132330-32ad38e42d3f // indirect
	k8s.io/utils v0.0.0-20241104163129-6fe5fd82f078
	sigs.k8s.io/json v0.0.0-20241014173422-cfa47c3a1cc8 // indirect
	sigs.k8s.io/structured-merge-diff/v4 v4.4.2 // indirect
	sigs.k8s.io/yaml v1.4.0
)

replace sigs.k8s.io/structured-merge-diff/v4 => sigs.k8s.io/structured-merge-diff/v4 v4.4.1
