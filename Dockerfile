# Build the manager binary
FROM golang:1.24-alpine AS builder

WORKDIR /workspace
ENV GOTOOLCHAIN=local
# Copy the Go Modules manifests
COPY go.mod go.mod
COPY go.sum go.sum
# cache deps before building and copying source so that we don't need to re-download as much
# and so that source changes don't invalidate our downloaded layer
RUN go mod download

COPY ./ /workspace/
ENV CGO_ENABLED=0 GOOS=linux GOARCH=amd64 GO111MODULE=on
RUN go build -ldflags="-s -w" -a -o cloud-profile-sync main.go

# Use distroless as minimal base image to package the manager binary
# Refer to https://github.com/GoogleContainerTools/distroless for more details
FROM gcr.io/distroless/static:nonroot
WORKDIR /
LABEL source_repository="https://github.com/cobaltcore-dev/cloud-profile-sync"
COPY --from=builder /workspace/cloud-profile-sync .
USER nonroot:nonroot

ENTRYPOINT ["/cloud-profile-sync"]