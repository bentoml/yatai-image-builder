# Developer Guide

I'm glad you can see this document and I'm looking forward to your contributions to the yatai-image-builder.

yatai-image-builder is a product based on a cloud-native architecture. It runs in a Kubernetes cluster, it is an operator for the BentoRequest CRD, it aims to generate the Bento CR, and it also acts as a client for Yatai to request Yatai's RESTful API.

As you know, Kubernetes has a complex network environment, so developing cloud-native products locally can be a challenge. But don't worry, this document will show you how to develop yatai-image-builder locally easily, quickly and comfortably.

## Prequisites

- A yatai-image-builder installed in the **development environment** for development and debugging

    > NOTE: Since you are developing, **you must not use the production environment**, so we recommend using the quick install script to install yatai and yatai-image-builder in the local minikube

    Using a development environment with pre-installed yatai-image-builder, the goal is to quickly provide a set of out-of-the-box infrastructure dependencies

    You can start by reading this [installation document](https://docs.bentoml.org/projects/yatai/en/latest/installation/yatai_deployment.html) to install yatai-image-builder. It is highly recommended to use the [quick install script](https://docs.bentoml.org/projects/yatai/en/latest/installation/yatai_deployment.html#quick-install) to install yatai-image-builder

    Remember, **never use infrastructure from the production environment**, only use newly installed infrastructure in the cluster, such as SQL databases, blob storage, docker registry, etc. The [quick install script](https://docs.bentoml.org/projects/yatai/en/latest/installation/yatai_deployment.html#quick-install) mentioned above will prevent you from using the infrastructure in the production environment, this script will help you to install all the infrastructure from scratch, you can use it without any worries.

    If you have already installed it, please verify that your kubectl context is correct with the following command:

    ```bash
    kubectl config current-context
    ```

- [jq](https://stedolan.github.io/jq/)

    Used to parse json from the command line

- [Go language compiler](https://go.dev/)

    yatai-image-builder is implemented by Go Programming Language

- [Telepresence](https://www.telepresence.io/)

    The most critical dependency in this document for bridging the local network and the Kubernetes cluster network

## Start Developing

<details>
1. Fork the yatai-image-builder project on [GitHub](https://github.com/bentoml/yatai-image-builder)

2. Clone the source code from your fork of yatai-image-builder's GitHub repository:

    ```bash
    git clone git@github.com:${your github username}/yatai-image-builder.git && cd yatai-image-builder
    ```

3. Add the yatai-image-builder upstream remote to your local yatai-image-builder clone:

    ```bash
    git remote add upstream git@github.com:bentoml/yatai-image-builder.git
    ```

4. Installing Go dependencies

    ```bash
    go mod download
    ```
</details>

## Making Changes

<details>
1. Make sure you're on the main branch.

   ```bash
   git checkout main
   ```

2. Use the git pull command to retrieve content from the BentoML Github repository.

   ```bash
   git pull upstream main -r
   ```

3. Create a new branch and switch to it.

   ```bash
   git checkout -b your-new-branch-name
   ```

4. Make your changes!

5. Use the git add command to save the state of files you have changed.

   ```bash
   git add <names of the files you have changed>
   ```

6. Commit your changes.

   ```bash
   git commit -m 'your commit message'
   ```

7. Synchronize upstream changes

    ```bash
    git pull upstream main -r
    ```

8. Push all changes to your forked repo on GitHub.

   ```bash
   git push origin your-new-branch-name
   ```
</details>

## Run yatai-image-builder

1. Run yatai-image-builder

    > WARNING: The following command uses the infrastructure of the Kubernetes environment in the current kubectl context and replaces the behavior of yatai-image-builder in the current Kubernetes environment, so please proceed with caution

    > NOTE: The following command will automatically run `telepresence connect` to connet to the k8s network

    ```bash
    make start-dev
    ```

    If you get something wrong, you should check the [Troubleshooting](#troubleshooting)

2. ✨ Enjoy it!

## Troubleshooting

You can test the telepresence connection with the following command:

```bash
curl http://yatai.yatai-system.svc.cluster.local/api/v1/info
```

The above command will return:

```bash
{"is_saas":false,"saas_domain_suffix":""}
```

If you can't communicate with the yatai service in the k8s cluster using the above command, you should kill all processes of telepresence:

```bash
ps aux | grep telepresence | grep -v grep | awk '{print $2}' | xargs -i sudo kill {}
```

Then run yatai-image-builder again:

```bash
make start-dev
```
