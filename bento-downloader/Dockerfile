FROM ubuntu:22.04

RUN apt-get update
RUN apt-get install -y curl jq unzip xz-utils file
RUN apt-get update && apt-get install -y \
    python3 \
    python3-pip \
    && apt-get clean \
    && rm -rf /var/lib/apt/lists/*

RUN pip3 install --upgrade pip
RUN pip3 install -U "huggingface_hub[cli]"
RUN curl "https://awscli.amazonaws.com/awscli-exe-linux-x86_64.zip" -o "awscliv2.zip" \
    && unzip awscliv2.zip \
    && ./aws/install \
    && rm awscliv2.zip \
    && rm -rf aws
RUN curl https://dl.google.com/dl/cloudsdk/channels/rapid/downloads/google-cloud-cli-410.0.0-linux-x86_64.tar.gz -o google-cloud-cli-410.tar.gz \
    && tar -xf google-cloud-cli-410.tar.gz \
    && ./google-cloud-sdk/install.sh \
    && rm google-cloud-cli-410.tar.gz

ENV PATH $PATH:/google-cloud-sdk/bin
