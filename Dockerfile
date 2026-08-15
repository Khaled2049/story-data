FROM golang:1.25-alpine AS build
WORKDIR /src
COPY go.mod go.sum* ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 go build -o /story-data ./cmd/api

FROM gcr.io/distroless/static-debian12
WORKDIR /app
COPY --from=build /story-data /story-data
COPY --from=build /src/migrations ./migrations
ENTRYPOINT ["/story-data"]
