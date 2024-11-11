FROM ghcr.io/blinklabs-io/go:1.23.3-1 AS build

WORKDIR /code
COPY go.* .
RUN go mod download
COPY . .
RUN make build

FROM debian:bookworm-slim AS node
COPY --from=build /code/node /bin/
COPY ./configs/cardano /opt/cardano/config
ENV CARDANO_CONFIG=/opt/cardano/config/preview/config.json
# Create database dir owned by container user
VOLUME /data/db
ENV CARDANO_DATABASE_PATH=/data/db
ENTRYPOINT ["node"]
