FROM --platform=$BUILDPLATFORM golang:1.27-alpine AS builder
ARG TARGETOS
ARG TARGETARCH
ARG TARGETVARIANT
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 GOOS=${TARGETOS:-linux} GOARCH=${TARGETARCH} GOARM=${TARGETVARIANT#v} \
    go build -trimpath -ldflags="-s -w" -o /out/gatusmod ./cmd/gatusmod

# Runs as root by default so it can write the generated endpoints file into a bind-mounted config directory.
# Override with `user:` in docker compose if the directory is writable for another uid.
FROM gcr.io/distroless/static-debian12
COPY --from=builder /out/gatusmod /gatusmod
ENV GATUSMOD_CONFIG_PATH=/config
ENV GATUSMOD_DATA_PATH=/data
EXPOSE 8080
HEALTHCHECK --interval=30s --timeout=5s --start-period=10s CMD ["/gatusmod", "healthcheck"]
ENTRYPOINT ["/gatusmod"]
