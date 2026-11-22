ARG GO_VERSION=1.27.1
FROM golang:${GO_VERSION}-alpine AS builder

WORKDIR /src

COPY go.mod go.sum* ./
RUN go mod download

COPY . .

ARG CMD_PATH=.
RUN CGO_ENABLED=0 GOOS=linux go build -trimpath -ldflags="-s -w" -o /out/app ${CMD_PATH}

FROM alpine:3

RUN apk add --no-cache ca-certificates tzdata

WORKDIR /app
COPY --from=builder /out/app /app/app

ENTRYPOINT ["/app/app"]
