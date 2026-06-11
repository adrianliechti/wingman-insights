# syntax=docker/dockerfile:1

FROM node:lts-alpine AS web

WORKDIR /src/web

COPY web/package*.json ./
RUN npm ci

COPY web/ ./
RUN npm run build


# golang:1 (debian) instead of alpine: duckdb-go ships prebuilt static
# libs linked against glibc, which fail to link under musl
FROM golang:1 AS build

WORKDIR /src

COPY go.* ./
RUN go mod download

COPY . .
COPY --from=web /src/web/dist ./web/dist
RUN CGO_ENABLED=1 go build -o /insights .


FROM debian:stable-slim

RUN apt-get update && \
    apt-get install -y --no-install-recommends tini ca-certificates mailcap && \
    rm -rf /var/lib/apt/lists/*

COPY --from=build /insights /

ENV INSIGHTS_DB_PATH=/data/insights.db
VOLUME /data

EXPOSE 4318

ENTRYPOINT ["/usr/bin/tini", "--"]
CMD ["/insights"]
