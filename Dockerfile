# Build stage (use full version to avoid cache pulling older Go)
FROM golang:1.25.7-alpine AS builder

ARG VERSION=dev
ARG GIT_COMMIT=unknown
ARG BUILD_DATE=unknown

WORKDIR /workspace

# Copy go mod files first for better caching
COPY go.mod go.sum ./
RUN go mod download

# Copy source code
COPY . .

# Build
RUN CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build \
    -trimpath \
    -ldflags="-s -w -X main.version=${VERSION} -X main.gitCommit=${GIT_COMMIT} -X main.buildDate=${BUILD_DATE}" \
    -o selfhealing-operator \
    ./cmd/operator

# Runtime stage
FROM gcr.io/distroless/static:nonroot

LABEL org.opencontainers.image.title="Self-Healing Operator"
LABEL org.opencontainers.image.description="Kubernetes operator for automated failure detection and remediation"
LABEL org.opencontainers.image.source="https://github.com/ariadna-ops/ariadna-self-healing"

WORKDIR /

COPY --from=builder /workspace/selfhealing-operator /selfhealing-operator

USER 65532:65532

ENTRYPOINT ["/selfhealing-operator"]

