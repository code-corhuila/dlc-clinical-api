# syntax=docker/dockerfile:1
FROM golang:1.26-alpine AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /out/clinical-api ./cmd/clinical-api \
 && CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /out/clinical-outbox-relay ./cmd/clinical-outbox-relay \
 && CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /out/clinical-inbox-consumer ./cmd/clinical-inbox-consumer \
 && CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /out/clinical-intent-reconciler ./cmd/clinical-intent-reconciler \n && CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /out/clinical-analytics-consumer ./cmd/clinical-analytics-consumer

FROM gcr.io/distroless/static-debian12:nonroot
COPY --from=build /out/clinical-api /clinical-api
COPY --from=build /out/clinical-outbox-relay /clinical-outbox-relay
COPY --from=build /out/clinical-inbox-consumer /clinical-inbox-consumer
COPY --from=build /out/clinical-intent-reconciler /clinical-intent-reconciler
COPY --from=build /out/clinical-analytics-consumer /clinical-analytics-consumer
EXPOSE 8080
USER nonroot:nonroot
ENTRYPOINT ["/clinical-api"]
