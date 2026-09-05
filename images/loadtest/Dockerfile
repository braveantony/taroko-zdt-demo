# loadtest client image:內含 oha(壓測)+ curl,常駐 sleep 待 kubectl exec。
# demo 時在 client pod 內 exec: oha -z 60s $TARGET  或  curl -s $TARGET
# oha-linux-amd64 為靜態 binary(ldd: statically linked),故 alpine base 可直接跑。
ARG OHA_VERSION=v1.16.0

FROM docker.io/library/alpine:3.21
ARG OHA_VERSION
ARG TARGETARCH=amd64
RUN apk add --no-cache curl ca-certificates \
 && curl -sSLf -o /usr/local/bin/oha \
      "https://github.com/hatoo/oha/releases/download/${OHA_VERSION}/oha-linux-${TARGETARCH}" \
 && chmod 0755 /usr/local/bin/oha \
 && adduser -D -u 65532 app
# 預設打 step0 的 hydra Service FQDN(namespace: zdt-tour)
ENV OHA_VERSION=${OHA_VERSION} \
    TARGET=http://hydra.zdt-tour.svc.cluster.local/version
USER 65532
# 常駐待命,實際壓測由 kubectl exec 觸發
ENTRYPOINT ["sleep", "infinity"]
