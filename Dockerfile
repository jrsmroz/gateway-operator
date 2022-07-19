FROM golang:1.18 as builder

WORKDIR /workspace

COPY go.mod go.mod
COPY go.sum go.sum

RUN go mod download

COPY main.go main.go
COPY apis/ apis/
COPY controllers/ controllers/
COPY pkg/ pkg/
COPY internal/ internal/

### Distroless/default
# Use distroless as minimal base image to package the operator binary
# Refer to https://github.com/GoogleContainerTools/distroless for more details
RUN CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -a -o manager main.go

FROM gcr.io/distroless/static:nonroot as distroless
WORKDIR /
COPY --from=builder /workspace/manager .
USER 65532:65532

ENTRYPOINT ["/manager"]

### RHEL
# Build UBI image
FROM registry.access.redhat.com/ubi8/ubi AS redhat
ARG TAG
ARG TARGETPLATFORM
ARG TARGETOS
ARG TARGETARCH

LABEL name="Kong Gateway Operator" \
      vendor="Kong" \
      version="$TAG" \
      release="1" \
      url="https://github.com/Kong/gateway-operator" \
      summary="A Kubernetes Operator for the Kong Gateway." \
      description="TODO"

# Create the user (ID 1000) and group that will be used in the
# running container to run the process as an unprivileged user.
RUN groupadd --system gateway-operator && \
    adduser --system gateway-operator -g gateway-operator -u 1000

COPY --from=builder /workspace/manager .
COPY LICENSE /licenses/

# Perform any further action as an unprivileged user.
USER 1000

# Run the compiled binary.
ENTRYPOINT ["/manager"]
