#!/bin/bash

set -xe

kubectl create ns yatai-system
kubectl create ns yatai-image-builder

echo "🚀 Creating AWS Secret Access Key..."
kubectl create secret generic aws-secret-access-key --from-literal=AWS_SECRET_ACCESS_KEY=${AWS_SECRET_ACCESS_KEY} --namespace yatai-image-builder
echo "🚀 Installing yatai-image-builder..."
YATAI_ENDPOINT='empty' USE_LOCAL_HELM_CHART=true UPGRADE_CRDS=false AWS_SECRET_ACCESS_KEY_EXISTING_SECRET_NAME=aws-secret-access-key AWS_SECRET_ACCESS_KEY_EXISTING_SECRET_KEY=AWS_SECRET_ACCESS_KEY bash ./scripts/quick-install-yatai-image-builder.sh
echo "yatai-image-builder helm release values:"
helm get values yatai-image-builder -n yatai-image-builder

