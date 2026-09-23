# Local mock authorize webhook + OpenMeter ingest (Compose profile: mock).
FROM golang:1.23-alpine AS build

WORKDIR /src
COPY mock-services/go.mod ./
COPY mock-services/main.go ./
RUN CGO_ENABLED=0 go build -o /mock-services .

FROM alpine:3.21
RUN apk add --no-cache ca-certificates

COPY --from=build /mock-services /mock-services

EXPOSE 8080
CMD ["/mock-services"]
