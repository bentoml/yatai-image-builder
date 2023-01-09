FROM ubuntu:22.04

RUN apt-get update
RUN apt-get install -y curl jq unzip xz-utils file
RUN curl "https://awscli.amazonaws.com/awscli-exe-linux-x86_64.zip" -o "awscliv2.zip" \
    && unzip awscliv2.zip \
    && ./aws/install \
    && rm awscliv2.zip \
    && rm -rf aws
RUN curl https://dl.google.com/dl/cloudsdk/channels/rapid/downloads/google-cloud-cli-410.0.0-linux-x86_64.tar.gz -o google-cloud-cli-410.tar.gz \
    && tar -xf google-cloud-cli-410.tar.gz \
    && ./google-cloud-sdk/install.sh \
    && rm google-cloud-cli-410.tar.gz

ARG USERNAME=yetone
ARG USER_UID=1000
ARG USER_GID=$USER_UID

# Create the user
RUN groupadd --gid $USER_GID $USERNAME \
    && useradd --uid $USER_UID --gid $USER_GID -m $USERNAME

USER $USER_UID:$USER_GID
