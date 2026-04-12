# syntax=docker/dockerfile:1.6

# ---- Frontend build ----
FROM node:20-alpine AS frontend
WORKDIR /web
COPY web/package.json web/package-lock.json* ./
RUN npm install
COPY web/ ./
RUN npm run build

# ---- Backend build ----
FROM golang:1.22-alpine AS backend
WORKDIR /src
RUN apk add --no-cache git
COPY go.mod go.sum* ./
RUN go mod download
COPY . .
COPY --from=frontend /web/build ./cmd/dockmesh/web_dist
ARG VERSION=dev
ARG COMMIT=none
ARG DATE=unknown
RUN CGO_ENABLED=0 go build -trimpath \
    -ldflags "-s -w \
      -X github.com/dockmesh/dockmesh/pkg/version.Version=${VERSION} \
      -X github.com/dockmesh/dockmesh/pkg/version.Commit=${COMMIT} \
      -X github.com/dockmesh/dockmesh/pkg/version.Date=${DATE}" \
    -o /out/dockmesh ./cmd/dockmesh

# ---- Runtime ----
FROM gcr.io/distroless/static-debian12:nonroot
WORKDIR /app
COPY --from=backend /out/dockmesh /app/dockmesh
EXPOSE 8080
USER nonroot:nonroot
ENTRYPOINT ["/app/dockmesh"]
