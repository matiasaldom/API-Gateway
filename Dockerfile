# One image with every command: gateway, apikey (CLI), and echo (demo upstream).
FROM golang:1.25-alpine AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY cmd ./cmd
COPY internal ./internal
RUN CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /out/ ./cmd/...

FROM alpine:3
RUN adduser -D -H app
COPY --from=build /out/ /usr/local/bin/
COPY gateway.yaml /etc/gateway/gateway.yaml
USER app
EXPOSE 8080
CMD ["gateway", "-config", "/etc/gateway/gateway.yaml"]
