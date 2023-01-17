#!/bin/bash

set -xe

kubectl create ns yatai-system
kubectl create ns yatai-image-builder
kubectl create ns yatai || true

echo "🚀 Creating AWS Secret Access Key..."
kubectl create secret generic aws-secret-access-key --from-literal=AWS_SECRET_ACCESS_KEY=${AWS_SECRET_ACCESS_KEY} --namespace yatai-image-builder
echo "🚀 Installing yatai-image-builder..."
YATAI_ENDPOINT='empty' USE_LOCAL_HELM_CHART=true UPGRADE_CRDS=false AWS_SECRET_ACCESS_KEY_EXISTING_SECRET_NAME=aws-secret-access-key AWS_SECRET_ACCESS_KEY_EXISTING_SECRET_KEY=AWS_SECRET_ACCESS_KEY bash ./scripts/quick-install-yatai-image-builder.sh
echo "yatai-image-builder helm release values:"
helm get values yatai-image-builder -n yatai-image-builder

helm upgrade --install docker-registry-1 twuni/docker-registry -n yatai-image-builder --set secrets.htpasswd='yetone:$2y$05$L7dlu/IGePjzo7C4YbmivOJxHs4jZ9k08D3CAAhxRQJoF62ey93yq'
cat <<EOF | kubectl apply -f -
apiVersion: v1
kind: Secret
metadata:
  name: regcred
  namespace: yatai
stringData:
  .dockerconfigjson: |
    {
      "auths": {
        "docker-registry-1.yatai-image-builder.svc.cluster.local:5000": {
          "auth": "$(echo -n 'yetone:password' | base64)"
        }
      }
    }
type: kubernetes.io/dockerconfigjson
EOF
