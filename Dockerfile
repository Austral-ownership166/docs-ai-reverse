# Multi-stage production image for docs-ai-reverse.
# Build: docker build -t docs-ai-reverse .
# Run:   docker run --rm -p 8317:8317 -v "$PWD/config.yaml:/config/config.yaml:ro" ghcr.io/6kmfi6hp/docs-ai-reverse:latest

FROM golang:1.26-bookworm AS builder

WORKDIR /src

COPY go.mod go.sum ./
RUN go mod download

COPY . .

ARG VERSION=dev
ARG COMMIT=none
ARG DATE=unknown

RUN CGO_ENABLED=0 GOOS=linux go build \
  -trimpath \
  -ldflags="-s -w -X main.version=${VERSION} -X main.commit=${COMMIT} -X main.date=${DATE}" \
  -o /out/docs-ai-reverse \
  .

FROM gcr.io/distroless/static-debian12:nonroot

WORKDIR /app

COPY --from=builder /out/docs-ai-reverse /usr/local/bin/docs-ai-reverse
COPY --from=builder /src/config.example.yaml /app/config.example.yaml

USER nonroot:nonroot

EXPOSE 8317

ENTRYPOINT ["/usr/local/bin/docs-ai-reverse"]
CMD ["/config/config.yaml"]
