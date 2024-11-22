FROM ghcr.io/blinklabs-io/go:1.23.3-1 AS build

WORKDIR /code
COPY go.* .
RUN go mod download
COPY . .
RUN make build

FROM debian:bookworm-slim AS dingo
COPY --from=build /code/dingo /bin/
COPY ./configs/cardano /opt/cardano/config
ENV CARDANO_CONFIG=/opt/cardano/config/preview/config.json
# Create database dir owned by container user
VOLUME /data/db
ENV CARDANO_DATABASE_PATH=/data/db
ENTRYPOINT ["dingo"]
