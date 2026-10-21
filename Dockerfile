FROM golang:1.26-alpine AS builder

WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /out/pulsefeed ./cmd/api

FROM alpine:3.21

RUN apk add --no-cache ca-certificates tzdata
WORKDIR /app
COPY --from=builder /out/pulsefeed ./pulsefeed

EXPOSE 8080
USER nobody
ENTRYPOINT ["./pulsefeed"]
