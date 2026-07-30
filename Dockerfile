FROM golang:1.26-bookworm AS build

WORKDIR /src
COPY go.mod ./
COPY cmd ./cmd
COPY internal ./internal
COPY cpp ./cpp
RUN CGO_ENABLED=0 GOOS=linux go build -trimpath -ldflags="-s -w" -o /out/loglab ./cmd/loglab
RUN g++ -std=c++17 -O2 -pthread -static-libstdc++ -static-libgcc -s -o /out/logwriter-cpp ./cpp/writer/main.cc

FROM debian:bookworm-slim

RUN mkdir -p /results /var/log/loglab && chown -R 65532:65532 /results /var/log/loglab
COPY --from=build /out/loglab /usr/local/bin/loglab
COPY --from=build /out/logwriter-cpp /usr/local/bin/logwriter-cpp
ENV LOGLAB_WRITER_EXECUTABLE=/usr/local/bin/logwriter-cpp
USER 65532:65532
ENTRYPOINT ["/usr/local/bin/loglab"]
