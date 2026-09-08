FROM golang:1.25-alpine AS build

WORKDIR /src
COPY main.go .
RUN CGO_ENABLED=0 go build -trimpath -o /fake-github main.go

FROM scratch
COPY --from=build /fake-github /fake-github
EXPOSE 8080
ENTRYPOINT ["/fake-github"]
