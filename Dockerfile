# syntax=docker/dockerfile:1
FROM golang:1.26-alpine AS build
WORKDIR /src
COPY go.mod go.sum* ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /out/uhpd ./cmd/uhpd

FROM alpine:3.20
RUN apk add --no-cache ca-certificates nodejs npm python3 py3-pip
# Install harness CLIs the router dispatches to. Add/remove as needed.
RUN npm install -g @anthropic-ai/claude-code opencode-ai || true
COPY --from=build /out/uhpd /usr/local/bin/uhpd
EXPOSE 8080
ENTRYPOINT ["/usr/local/bin/uhpd"]
