# Runtime image for the XAppEnvironment composition function.
#
# This is the image embedded into the function package by
# `crossplane xpkg build --embed-runtime-image`.

FROM golang:1.26 AS build

WORKDIR /src

# Download dependencies first so they cache independently of source changes.
COPY go.mod go.sum ./
RUN go mod download

COPY fn/ fn/
# The function imports the advisory policy client from ./internal/policy.
COPY internal/ internal/

# CGO_ENABLED=0 keeps the binary static so it runs on a distroless base.
ARG TARGETOS=linux
ARG TARGETARCH
RUN CGO_ENABLED=0 GOOS=${TARGETOS} GOARCH=${TARGETARCH} \
    go build -o /function ./fn

FROM gcr.io/distroless/static-debian12:nonroot

COPY --from=build /function /function

EXPOSE 9443
USER nonroot:nonroot
ENTRYPOINT ["/function"]
