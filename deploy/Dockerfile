# zaweb, the hub.  Built from source so CI needs nothing but Docker; the
# context is zawiper/.  The image holds one static binary: no shell, no
# libc, no CA bundle (the bay links use our own CA, mounted at run time).
#
#   docker build -f deploy/Dockerfile -t zawiper/zaweb:latest .
FROM golang:1.24-alpine AS build
WORKDIR /src
COPY go.mod ./
COPY cmd ./cmd
COPY internal ./internal
ARG VERSION=dev
ARG COMMIT=unknown
RUN CGO_ENABLED=0 go build -trimpath \
      -ldflags "-s -w -X main.version=${VERSION} -X main.commit=${COMMIT}" \
      -o /zaweb ./cmd/zaweb

FROM scratch
COPY --from=build /zaweb /zaweb
USER 8460:8460
# 8460: the page, behind Caddy.  8461: the bay links, mutual TLS, direct.
EXPOSE 8460 8461
ENTRYPOINT ["/zaweb"]
