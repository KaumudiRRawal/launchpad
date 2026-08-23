# The control-plane image.
#
# Build it from the repository root, not from control-plane/:
#
#     docker build -f control-plane/Dockerfile -t launchpad-control-plane .
#
# The Go toolchain stamps the VCS revision into the binary from the .git
# directory above the module, and that stamp is what /v1/version reports. A
# build scoped to control-plane/ alone cannot see it, and an image that answers
# "dev" when asked which commit it is running defeats the endpoint's purpose.

FROM golang:1.26-alpine AS build

# git is needed at build time only so the toolchain can read the revision.
RUN apk add --no-cache git

WORKDIR /src/control-plane
COPY control-plane/go.mod control-plane/go.sum ./
RUN go mod download

COPY .git /src/.git
COPY control-plane/ ./

# Statically linked, so the binary does not have to match the libc of the image
# it is copied into. -trimpath keeps build-machine paths out of panic traces.
RUN CGO_ENABLED=0 go build -trimpath -o /out/launchpad-api ./cmd/api \
	&& CGO_ENABLED=0 go build -trimpath -o /out/launchpad-bootstrap ./cmd/bootstrap

# The runtime image carries the Google Cloud CLI because the Cloud Run driver
# shells out to it, and git because the fetcher does. That is the price of
# shelling out to vendor CLIs instead of linking their client libraries, and it
# is paid here, once, rather than in a dependency tree that every developer
# builds through.
FROM gcr.io/google.com/cloudsdktool/google-cloud-cli:alpine

RUN apk add --no-cache git \
	&& adduser -D -u 10001 launchpad

COPY --from=build /out/launchpad-api /usr/local/bin/launchpad-api
COPY --from=build /out/launchpad-bootstrap /usr/local/bin/launchpad-bootstrap

# The platform builds and runs other people's code. It does not need root to do
# it, and the deploy worker writes checkouts to a temporary directory this user
# owns rather than anywhere in the image.
USER launchpad
ENV HOME=/home/launchpad

# Matches the default in LAUNCHPAD_HTTP_ADDR and Cloud Run's own default, so a
# container started with no configuration at all still answers where the
# platform expects. The proxy listens separately on LAUNCHPAD_PROXY_ADDR; a
# runtime that publishes one port per service publishes the other one from a
# second instance of this same image.
EXPOSE 8080

ENTRYPOINT ["/usr/local/bin/launchpad-api"]
