FROM --platform=$BUILDPLATFORM golang:1.26-alpine AS builder
ARG TARGETARCH
WORKDIR /app
COPY . .
RUN CGO_ENABLED=0 GOOS=linux GOARCH=${TARGETARCH} go build -ldflags '-w -s' -o listener ./cmd/listener
RUN CGO_ENABLED=0 GOOS=linux GOARCH=${TARGETARCH} go build -ldflags '-w -s' -o gateway ./cmd/gateway

FROM gcr.io/distroless/static-debian12:nonroot
WORKDIR /app
COPY --from=builder /app/listener /app/gateway ./
USER nonroot:nonroot
ENTRYPOINT ["/app/listener"]
