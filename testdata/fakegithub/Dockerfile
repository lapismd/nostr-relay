FROM golang:1.25-alpine AS build

WORKDIR /src
COPY main.go .
RUN apk add --no-cache git
RUN git config --global user.email fake-github@example.invalid && \
    git config --global user.name "Fake GitHub" && \
    git init --bare /git-data && \
    git init -b main /work && \
    printf '# fake GitHub v1\n' > /work/README.md && \
    git -C /work add README.md && git -C /work commit -m initial && \
    git -C /work tag v1 && \
    printf '# fake GitHub v2\n' > /work/README.md && \
    printf 'replicated content\n' > /work/web.txt && \
    git -C /work add . && git -C /work commit -m update && \
    git -C /work remote add origin /git-data && \
    git -C /work push origin main --tags && \
    git --git-dir=/git-data symbolic-ref HEAD refs/heads/main && \
    git --git-dir=/git-data update-server-info
RUN CGO_ENABLED=0 go build -trimpath -o /fake-github main.go

FROM scratch
COPY --from=build /fake-github /fake-github
COPY --from=build /git-data /git-data
EXPOSE 8080
ENTRYPOINT ["/fake-github"]
