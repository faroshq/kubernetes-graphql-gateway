FROM --platform=$BUILDPLATFORM golang:1.26-alpine AS builder
ARG TARGETARCH

WORKDIR /app
COPY . .
RUN CGO_ENABLED=0 GOOS=linux GOARCH=${TARGETARCH} go build -ldflags '-w -s' -o /out/listener ./cmd/listener
RUN CGO_ENABLED=0 GOOS=linux GOARCH=${TARGETARCH} go build -ldflags '-w -s' -o /out/gateway ./cmd/gateway

FROM gcr.io/distroless/static-debian12:nonroot
WORKDIR /app
COPY --from=builder /out/listener /out/gateway ./
USER nonroot:nonroot
ENTRYPOINT ["/app/listener"]
