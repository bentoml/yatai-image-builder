#!/bin/bash

CURRENT_CONTEXT=$(kubectl config current-context)
echo -e "\033[01;31mWarning: this will permanently delete all yatai-image-builder resources, existing bento deployments, docker image data. Note that external docker image will not be deleted.\033[00m"
echo -e "\033[01;31mWarning: this also means that all resources under the \033[00m\033[01;32myatai-image-builder\033[00m \033[01;31mnamespaces will be permanently deleted.\033[00m"
echo -e "\033[01;31mCurrent kubernetes context: \033[00m\033[01;32m$CURRENT_CONTEXT\033[00m"


while true; do
  echo -e -n "Are you sure to delete yatai-image-builder in cluster \033[00m\033[01;32m${CURRENT_CONTEXT}\033[00m? [y/n] "
  read yn
  case $yn in
    [Yy]* ) break;;
    [Nn]* ) exit;;
    * ) echo "Please answer yes or no.";;
  esac
done

echo "Uninstalling yatai-image-builder helm chart from cluster.."
set -x
helm list -n yatai-image-builder | tail -n +2 | awk '{print $1}' | xargs -I{} helm -n yatai-image-builder uninstall {}
set +x

echo "Removing additional yatai-image-builder related namespaces and resources.."
set -x
kubectl delete namespace yatai-image-builder
set +x

echo "Done"
