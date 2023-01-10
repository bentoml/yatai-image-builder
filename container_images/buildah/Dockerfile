FROM quay.io/buildah/stable:v1.23.1

RUN touch /etc/subgid /etc/subuid \
 && chmod g=u /etc/subgid /etc/subuid /etc/passwd \
 && echo build:10000:65536 > /etc/subuid \
 && echo build:10000:65536 > /etc/subgid

ENV BUILDAH_ISOLATION=chroot

ARG BENTO_USER=bentoml
ARG BENTO_USER_UID=1034
ARG BENTO_USER_GID=1034
RUN groupadd -g $BENTO_USER_GID -o $BENTO_USER && useradd -m -u $BENTO_USER_UID -g $BENTO_USER_GID -o -r $BENTO_USER

RUN echo $BENTO_USER:$BENTO_USER_UID:$BENTO_USER_GID >> /etc/subuid \
  && echo $BENTO_USER:$BENTO_USER_UID:$BENTO_USER_GID >> /etc/subgid

RUN echo "export BUILDAH_ISOLATION=chroot" >> /home/build/.bashrc

RUN mkdir -p /home/build/.config/containers \
 && (echo '[storage]';echo 'driver = "vfs"') > /home/build/.config/containers/storage.conf

USER build
WORKDIR /home/build

