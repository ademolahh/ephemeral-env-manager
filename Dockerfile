FROM golang:1.27.2 AS build
WORKDIR /src
COPY go.mod go.sum* ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /out/ ./cmd/...
RUN CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /out/tools/ ./scripts/create_admin ./scripts/seed_docker && \
    CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /out/tools/generate_api_key ./scripts

FROM gcr.io/distroless/static-debian12:nonroot AS tools
COPY --from=build /out/tools/ /tools/
USER nonroot:nonroot

FROM gcr.io/distroless/static-debian12:nonroot AS worker
COPY --from=build /out/worker /worker
USER nonroot:nonroot
ENTRYPOINT ["/worker"]

FROM gcr.io/distroless/static-debian12:nonroot AS eem
COPY --from=build /out/eem /eem
USER nonroot:nonroot
ENTRYPOINT ["/eem"]

FROM gcr.io/distroless/static-debian12:nonroot AS proxy
COPY --from=build /out/proxy /proxy
USER nonroot:nonroot
ENTRYPOINT ["/proxy"]
