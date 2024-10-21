FROM ghcr.io/blinklabs-io/go:1.23.2-1 AS build

WORKDIR /code
COPY go.* .
RUN go mod download
COPY . .
RUN make build

FROM debian:bookworm-slim AS node
COPY --from=build /code/node /bin/
COPY ./configs/cardano /opt/cardano/config
ENV CARDANO_CONFIG=/opt/cardano/config/preview/config.json
# Create data dir owned by container user and use it as default dir
VOLUME /data
WORKDIR /data
ENV CARDANO_DATA_DIR=/data
ENTRYPOINT ["node"]
