# One Dockerfile, parameterized by CMD_PATH, builds any of the four
# deployment-unit commands (docs/SCAFFOLD_BOUNDARIES.md). A single
# source of truth for the build recipe is deliberate — four near-
# identical Dockerfiles would drift out of sync with each other over
# time; this can't.
#
# Build:
#   docker build --build-arg CMD_PATH=cmd/creation -t url-shortener-creation:local .
#   docker build --build-arg CMD_PATH=cmd/redirect -t url-shortener-redirect:local .
#   docker build --build-arg CMD_PATH=cmd/admin    -t url-shortener-admin:local .
#   docker build --build-arg CMD_PATH=cmd/worker   -t url-shortener-worker:local .

ARG GO_VERSION=1.25

FROM golang:${GO_VERSION}-alpine AS build
ARG CMD_PATH
WORKDIR /src

# Dependency layer cached separately from source so an app-only change
# doesn't re-download modules.
COPY go.mod go.sum ./
RUN go mod download

COPY . .
RUN CGO_ENABLED=0 GOOS=linux go build -trimpath -ldflags="-s -w" -o /out/app ./${CMD_PATH}

# Distroless "nonroot" base: no shell, no package manager, no root user
# available at all — not just "don't run as root", the image contains
# nothing that *could* run as root (NFR-SEC-004, NFR-MNT-001).
FROM gcr.io/distroless/static-debian12:nonroot
COPY --from=build /out/app /app
USER nonroot:nonroot
ENTRYPOINT ["/app"]
