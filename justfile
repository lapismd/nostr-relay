run:
  go run cmd/relay/main.go

build:
  CGO_ENABLED=0 go build -o bin/lapis-relay cmd/relay/main.go

test:
  go test -v ./...

fmt:
  gofmt -w -s .

spec-build:
  mdbook build spec

spec-check:
  go test ./internal/specdocs
  mdbook build spec

spec-serve:
  mdbook serve spec
