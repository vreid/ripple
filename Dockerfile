FROM golang:1.24 AS build

WORKDIR /build

COPY go.mod .
COPY go.sum .

RUN go mod download

COPY ripple_bot_prompt.md .

COPY main.go .
COPY ripple.go .

ARG CGO_ENABLED=0
RUN go build -ldflags "-s -w"

FROM scratch

WORKDIR /app

COPY --from=build /build/ripple .

ENTRYPOINT ["/app/ripple"]