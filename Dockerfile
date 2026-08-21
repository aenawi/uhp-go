# syntax=docker/dockerfile:1
FROM golang:1.26-alpine AS build
WORKDIR /src
COPY go.mod go.sum* ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /out/uhpd ./cmd/uhpd

FROM alpine:3.20
RUN apk add --no-cache ca-certificates nodejs npm

# Install harness CLIs the router dispatches to. Add/remove as needed.
#
# Deliberately not suffixed with `|| true`. An image that ships without the CLIs
# it claims to ship is a broken image, and letting the build go green means
# finding that out at request time, with a client already waiting on the answer.
RUN npm install -g @anthropic-ai/claude-code opencode-ai

# Harness CLIs run commands on behalf of authenticated clients. That is the
# product; it should not be the product as uid 0. The id is pinned so a
# deployment bind-mounting a host directory over the workspace has a fixed
# number to chown it to.
RUN addgroup -g 10001 -S uhp && adduser -u 10001 -G uhp -S -h /home/uhp uhp
# Harness CLIs keep their credentials and state under $HOME, so it has to be a
# directory this user can write, not the `/` a bare USER directive leaves behind.
ENV HOME=/home/uhp

# Every session gets a working directory under UHP_WORKSPACE, so the image ships
# one the runtime user owns rather than leaving the first session to fail on a
# directory it cannot create. Declared as a volume because sessions, uploaded
# files and the harness store belong to the deployment, not to an image layer.
ENV UHP_WORKSPACE=/workspace
RUN mkdir -p /workspace && chown uhp:uhp /workspace
VOLUME ["/workspace"]

COPY --from=build /out/uhpd /usr/local/bin/uhpd
USER uhp
WORKDIR /workspace
EXPOSE 8080
ENTRYPOINT ["/usr/local/bin/uhpd"]
