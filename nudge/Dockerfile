# Build context: the nudge/ directory.
FROM golang:1.27 AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /out/nudge ./cmd/nudge

FROM gcr.io/distroless/static-debian12:nonroot
WORKDIR /app
COPY --from=build /out/nudge /app/nudge
COPY experiments.yaml /app/experiments.yaml
ENV EXPERIMENTS_PATH=/app/experiments.yaml
USER nonroot:nonroot
ENTRYPOINT ["/app/nudge"]
