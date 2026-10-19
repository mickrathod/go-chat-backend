# ------------------------------------------------------------------------------
# Go WebSocket Backend Container
# ------------------------------------------------------------------------------
FROM golang:1.24-alpine AS builder
WORKDIR /app

RUN apk add --no-cache ca-certificates git

ENV GOTOOLCHAIN=auto

COPY go.mod go.sum ./
RUN go mod download

COPY . ./

RUN CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -ldflags="-s -w" -o /gochat-server main.go

# ------------------------------------------------------------------------------
# Minimal runtime image (< 20 MB)
# ------------------------------------------------------------------------------
FROM alpine:3.19
WORKDIR /app

RUN apk add --no-cache ca-certificates tzdata

COPY --from=builder /gochat-server /app/server
COPY sdk /app/sdk

RUN mkdir -p /app/uploads

ENV PORT=8080
EXPOSE 8080

CMD ["/app/server"]
