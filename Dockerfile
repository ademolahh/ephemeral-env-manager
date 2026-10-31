FROM golang:1.27.2 AS build
WORKDIR /src
COPY go.mod go.sum* ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /out/eem ./cmd/eem

FROM gcr.io/distroless/static-debian12:nonroot
COPY --from=build /out/eem /eem
USER nonroot:nonroot
ENTRYPOINT ["/eem"]
