# 工具箱 image:把 demo 對應版本的 kind / kubectl / cilium-cli / helm 打包進來,
# run 一次就把四個 binary 裝到 host 的 ~/.local/bin(見 entrypoint.sh)。
ARG KIND_VERSION=v0.32.0
ARG KUBECTL_VERSION=v1.36.1
ARG CILIUM_CLI_VERSION=v0.20.0
ARG HELM_VERSION=v3.21.4

FROM docker.io/library/alpine:3.21 AS build
ARG KIND_VERSION
ARG KUBECTL_VERSION
ARG CILIUM_CLI_VERSION
ARG HELM_VERSION
ARG TARGETARCH=amd64
RUN apk add --no-cache curl tar
WORKDIR /tools
RUN set -eux; \
    curl -sSLf -o kind "https://github.com/kubernetes-sigs/kind/releases/download/${KIND_VERSION}/kind-linux-${TARGETARCH}"; \
    curl -sSLf -o kubectl "https://dl.k8s.io/release/${KUBECTL_VERSION}/bin/linux/${TARGETARCH}/kubectl"; \
    curl -sSLf -o cilium.tgz "https://github.com/cilium/cilium-cli/releases/download/${CILIUM_CLI_VERSION}/cilium-linux-${TARGETARCH}.tar.gz"; \
    tar -xzf cilium.tgz cilium; rm cilium.tgz; \
    curl -sSLf -o helm.tgz "https://get.helm.sh/helm-${HELM_VERSION}-linux-${TARGETARCH}.tar.gz"; \
    tar -xzf helm.tgz --strip-components=1 "linux-${TARGETARCH}/helm"; rm helm.tgz; \
    chmod +x kind kubectl cilium helm

FROM docker.io/library/alpine:3.21
ARG KIND_VERSION
ARG KUBECTL_VERSION
ARG CILIUM_CLI_VERSION
ARG HELM_VERSION
ENV KIND_VERSION=${KIND_VERSION} \
    KUBECTL_VERSION=${KUBECTL_VERSION} \
    CILIUM_CLI_VERSION=${CILIUM_CLI_VERSION} \
    HELM_VERSION=${HELM_VERSION}
COPY --from=build /tools/kind /tools/kubectl /tools/cilium /tools/helm /tools/
COPY entrypoint.sh /entrypoint.sh
RUN chmod +x /entrypoint.sh
ENTRYPOINT ["/entrypoint.sh"]
