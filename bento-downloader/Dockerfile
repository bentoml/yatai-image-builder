FROM ubuntu:22.04

RUN apt-get update
RUN apt-get install -y curl jq unzip xz-utils file
RUN apt-get update && apt-get install -y \
    python3 \
    python3-pip \
    && apt-get clean \
    && rm -rf /var/lib/apt/lists/*

# pip < 26.2.2 trusts the compromised eMudhra CAs; the hash keeps a MITM from swapping the wheel
RUN pip3 install "pip @ https://files.pythonhosted.org/packages/3f/06/3e4420302363fbb71be3c7f35ce042f54bfca330e3e66b3a2e3c4cca579f/pip-26.2.2-py3-none-any.whl#sha256=fd7f4dfa8190e014013b9f08f3e973f4e4c26ffd179a204895533a7254813a70"
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
