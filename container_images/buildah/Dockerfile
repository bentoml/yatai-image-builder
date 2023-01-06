FROM quay.io/buildah/stable:v1.23.1

RUN touch /etc/subgid /etc/subuid \
 && chmod g=u /etc/subgid /etc/subuid /etc/passwd \
 && echo build:10000:65536 > /etc/subuid \
 && echo build:10000:65536 > /etc/subgid

ENV BUILDAH_ISOLATION=chroot

USER build
WORKDIR /home/build

