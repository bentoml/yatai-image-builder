#!/bin/bash

set -e

DEVEL=${DEVEL:-false}
DEVEL_HELM_REPO=${DEVEL_HELM_REPO:-false}

is_minikube=false
if kubectl config view --minify | grep 'minikube.sigs.k8s.io' > /dev/null; then
  is_minikube=true
fi

# check if jq command exists
if ! command -v jq &> /dev/null; then
  arch=$(uname -m)
  # download jq from github by different arch
  if [[ $arch == "x86_64" && $OSTYPE == 'darwin'* ]]; then
    jq_archived_name="gojq_v0.12.9_darwin_amd64"
  elif [[ $arch == "arm64" && $OSTYPE == 'darwin'* ]]; then
    jq_archived_name="gojq_v0.12.9_darwin_arm64"
  elif [[ $arch == "x86_64" && $OSTYPE == 'linux'* ]]; then
    jq_archived_name="gojq_v0.12.9_linux_amd64"
  elif [[ $arch == "aarch64" && $OSTYPE == 'linux'* ]]; then
    jq_archived_name="gojq_v0.12.9_linux_arm64"
  else
    echo "jq command not found, please install it first"
    exit 1
  fi
  echo "📥 downloading jq from github"
  if [[ $OSTYPE == 'darwin'* ]]; then
    curl -sL -o /tmp/yatai-jq.zip "https://github.com/itchyny/gojq/releases/download/v0.12.9/${jq_archived_name}.zip"
    echo "✅ downloaded jq to /tmp/yatai-jq.zip"
    echo "📦 extracting yatai-jq.zip"
    unzip -q /tmp/yatai-jq.zip -d /tmp
  else
    curl -sL -o /tmp/yatai-jq.tar.gz "https://github.com/itchyny/gojq/releases/download/v0.12.9/${jq_archived_name}.tar.gz"
    echo "✅ downloaded jq to /tmp/yatai-jq.tar.gz"
    echo "📦 extracting yatai-jq.tar.gz"
    tar zxf /tmp/yatai-jq.tar.gz -C /tmp
  fi
  echo "✅ extracted jq to /tmp/${jq_archived_name}"
  jq="/tmp/${jq_archived_name}/gojq"
else
  jq=$(which jq)
fi

# check if kubectl command exists
if ! command -v kubectl >/dev/null 2>&1; then
  echo "😱 kubectl command is not found, please install it first!" >&2
  exit 1
fi

KUBE_VERSION=$(kubectl version --output=json | $jq '.serverVersion.minor')
if [ ${KUBE_VERSION:1:2} -lt 20 ]; then
  echo "😱 install requires at least Kubernetes 1.20" >&2
  exit 1
fi

# check if helm command exists
if ! command -v helm >/dev/null 2>&1; then
  echo "😱 helm command is not found, please install it first!" >&2
  exit 1
fi

echo "🧪 verifying that the yatai is running"
if ! kubectl -n yatai-system wait --for=condition=ready --timeout=10s pod -l app.kubernetes.io/name=yatai; then
  echo "😱 yatai is not ready, please wait for it to be ready!" >&2
  exit 1
fi
echo "✅ yatai is ready"

namespace=yatai-image-builder

# check if namespace exists
if ! kubectl get namespace ${namespace} >/dev/null 2>&1; then
  echo "🤖 creating namespace ${namespace}"
  kubectl create namespace ${namespace}
  echo "✅ namespace ${namespace} created"
fi

new_cert_manager=0

if [ $(kubectl get pod -A -l app=cert-manager 2> /dev/null | wc -l) = 0 ]; then
  new_cert_manager=1
  echo "🤖 installing cert-manager..."
  kubectl apply -f https://github.com/cert-manager/cert-manager/releases/download/v1.9.1/cert-manager.yaml
else
  echo "😀 cert-manager is already installed"
fi

echo "⏳ waiting for cert-manager to be ready..."
kubectl wait --for=condition=ready --timeout=600s pod -l app.kubernetes.io/instance=cert-manager -A
echo "✅ cert-manager is ready"

if [ ${new_cert_manager} = 1 ]; then
  echo "😴 sleep 10s to make cert-manager really work 🤷"
  sleep 10
  echo "✨ wake up"
fi

cat <<EOF > /tmp/cert-manager-test-resources.yaml
apiVersion: cert-manager.io/v1
kind: Issuer
metadata:
  name: test-selfsigned
  namespace: ${namespace}
spec:
  selfSigned: {}
---
apiVersion: cert-manager.io/v1
kind: Certificate
metadata:
  name: selfsigned-cert
  namespace: ${namespace}
spec:
  dnsNames:
    - example.com
  secretName: selfsigned-cert-tls
  issuerRef:
    name: test-selfsigned
EOF

kubectl apply -f /tmp/cert-manager-test-resources.yaml
echo "🧪 verifying that the cert-manager is working properly"
sleep 5
if ! kubectl -n ${namespace} wait --for=condition=ready --timeout=30s certificate selfsigned-cert; then
  echo "😱 self-signed certificate is not issued, please check cert-manager installation!" >&2
  exit 1;
fi
kubectl delete -f /tmp/cert-manager-test-resources.yaml
echo "✅ cert-manager is working properly"

if [ $(kubectl get pod -A -l k8s-app=metrics-server 2> /dev/null | wc -l) = 0 ]; then
  echo "🤖 installing metrics-server..."
  if [ "${is_minikube}" = "true" ]; then
    minikube addons enable metrics-server
  else
    kubectl apply -f https://github.com/kubernetes-sigs/metrics-server/releases/latest/download/components.yaml
  fi
else
  echo "😀 metrics-server is already installed"
fi

echo "⏳ waiting for metrics-server to be ready..."
kubectl wait --for=condition=ready --timeout=600s pod -l k8s-app=metrics-server -A
echo "✅ metrics-server is ready"

helm repo add twuni https://helm.twun.io
helm repo update twuni
echo "🤖 installing docker-registry..."
helm upgrade --install docker-registry twuni/docker-registry -n ${namespace}

echo "⏳ waiting for docker-registry to be ready..."
kubectl -n ${namespace} wait --for=condition=ready --timeout=600s pod -l app=docker-registry
echo "✅ docker-registry is ready"

echo "🤖 installing docker-private-registry-proxy..."
cat <<EOF | kubectl apply -f -
apiVersion: apps/v1
kind: DaemonSet
metadata:
  name: docker-private-registry-proxy
  namespace: ${namespace}
  labels:
    app: docker-private-registry-proxy
spec:
  selector:
    matchLabels:
      app: docker-private-registry-proxy
  template:
    metadata:
      creationTimestamp: null
      labels:
        app: docker-private-registry-proxy
    spec:
      containers:
      - args:
        - tcp
        - "5000"
        - docker-registry.${namespace}.svc.cluster.local
        image: quay.io/bentoml/proxy-to-service:v2
        name: tcp-proxy
        ports:
        - containerPort: 5000
          hostPort: 5000
          name: tcp
          protocol: TCP
        resources:
          limits:
            cpu: 100m
            memory: 100Mi
EOF

echo "⏳ waiting for docker-private-registry-proxy to be ready..."
kubectl -n ${namespace} wait --for=condition=ready --timeout=600s pod -l app=docker-private-registry-proxy
echo "✅ docker-private-registry-proxy is ready"

DOCKER_REGISTRY_SERVER=127.0.0.1:5000
DOCKER_REGISTRY_IN_CLUSTER_SERVER=docker-registry.${namespace}.svc.cluster.local:5000
DOCKER_REGISTRY_USERNAME=''
DOCKER_REGISTRY_PASSWORD=''
DOCKER_REGISTRY_SECURE=false
DOCKER_REGISTRY_BENTO_REPOSITORY_NAME=yatai-bentos

helm_repo_name=bentoml
helm_repo_url=https://bentoml.github.io/helm-charts

# check if DEVEL_HELM_REPO is true
if [ "${DEVEL_HELM_REPO}" = "true" ]; then
  helm_repo_name=bentoml-devel
  helm_repo_url=https://bentoml.github.io/helm-charts-devel
fi

UPGRADE_CRDS=${UPGRADE_CRDS:-false}

if [ "${UPGRADE_CRDS}" = "true" ]; then
  echo "🤖 installing yatai-image-builder CRDs..."
  kubectl apply -f https://raw.githubusercontent.com/bentoml/yatai-image-builder/main/helm/yatai-image-builder/crds/bentorequest.yaml
  echo "⏳ waiting for BentoRequest CRD to be established..."
  kubectl wait --for condition=established --timeout=120s crd/bentorequests.resources.yatai.ai
  echo "✅ BentoRequest CRD are established"
  echo "⏳ waiting for Bento CRD to be established..."
  kubectl wait --for condition=established --timeout=120s crd/bentos.resources.yatai.ai
  echo "✅ Bento CRD are established"
fi

helm repo remove ${helm_repo_name} 2> /dev/null || true
helm repo add ${helm_repo_name} ${helm_repo_url}
helm repo update ${helm_repo_name}
echo "🤖 installing yatai-image-builder..."
helm upgrade --install yatai-image-builder ${helm_repo_name}/yatai-image-builder -n ${namespace} \
  --set dockerRegistry.server=$DOCKER_REGISTRY_SERVER \
  --set dockerRegistry.inClusterServer=$DOCKER_REGISTRY_IN_CLUSTER_SERVER \
  --set dockerRegistry.username=$DOCKER_REGISTRY_USERNAME \
  --set dockerRegistry.password=$DOCKER_REGISTRY_PASSWORD \
  --set dockerRegistry.secure=$DOCKER_REGISTRY_SECURE \
  --set dockerRegistry.bentoRepositoryName=$DOCKER_REGISTRY_BENTO_REPOSITORY_NAME \
  --skip-crds=${UPGRADE_CRDS} \
  --devel=${DEVEL}

kubectl -n ${namespace} rollout restart deploy/yatai-image-builder

echo "⏳ waiting for yatai-image-builder to be ready..."
kubectl -n ${namespace} wait --for=condition=available --timeout=600s deploy/yatai-image-builder
echo "✅ yatai-image-builder is ready"
