FROM golang:1.27.2 AS build
WORKDIR /src
COPY go.mod go.sum* ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /out/ ./cmd/...

FROM gcr.io/distroless/static-debian12:nonroot AS worker
COPY --from=build /out/worker /worker
USER nonroot:nonroot
ENTRYPOINT ["/worker"]

FROM gcr.io/distroless/static-debian12:nonroot AS eem
COPY --from=build /out/eem /eem
USER nonroot:nonroot
ENTRYPOINT ["/eem"]
