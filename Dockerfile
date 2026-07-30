FROM golang:1.26-bookworm AS build

WORKDIR /src
COPY go.mod ./
COPY cmd ./cmd
COPY internal ./internal
RUN CGO_ENABLED=0 GOOS=linux go build -trimpath -ldflags="-s -w" -o /out/loglab ./cmd/loglab

FROM debian:bookworm-slim

RUN mkdir -p /results /var/log/loglab && chown -R 65532:65532 /results /var/log/loglab
COPY --from=build /out/loglab /usr/local/bin/loglab
USER 65532:65532
ENTRYPOINT ["/usr/local/bin/loglab"]
