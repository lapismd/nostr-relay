FROM golang:1.25 AS build

WORKDIR /app

COPY go.mod go.sum ./
COPY modules/buzzagents/go.mod modules/buzzagents/go.sum modules/buzzagents/

RUN go mod download

COPY internal internal
COPY modules/buzzagents modules/buzzagents
COPY zooid zooid
COPY cmd cmd

RUN CGO_ENABLED=0 GOOS=linux go build -o bin/lapis-relay cmd/relay/main.go && \
    CGO_ENABLED=0 GOOS=linux go build -o bin/lapis-forge-worker cmd/forge-worker/main.go

FROM alpine:3.20 AS runtime

RUN apk add --no-cache ca-certificates git git-lfs

WORKDIR /app

COPY --from=build /app/bin/lapis-relay /bin/lapis-relay
COPY --from=build /app/bin/lapis-forge-worker /bin/lapis-forge-worker
COPY templates /app/templates
COPY static /app/static
COPY docker-entrypoint.sh /docker-entrypoint.sh

RUN chmod +x /docker-entrypoint.sh && \
    mkdir -p /app/config /app/media && \
    chown -R nobody:nogroup /app

USER nobody

EXPOSE 3334

ENV PORT=3334
ENV CONFIG=/app/config
ENV MEDIA=/app/media

ENTRYPOINT ["/docker-entrypoint.sh"]

FROM runtime AS forge-worker

ENV LAPIS_PROCESS_BIN=/bin/lapis-forge-worker

FROM runtime AS relay

ENV LAPIS_PROCESS_BIN=/bin/lapis-relay
