FROM golang:1.26-alpine AS build

WORKDIR /src
COPY go.mod ./
RUN go mod download
COPY . .
RUN go build -trimpath -ldflags="-s -w" -o /out/clinical-api ./cmd/clinical-api

FROM alpine:3.22
RUN addgroup -S clinical && adduser -S clinical -G clinical
USER clinical
COPY --from=build /out/clinical-api /clinical-api
EXPOSE 8080
HEALTHCHECK --interval=10s --timeout=3s --start-period=5s --retries=3 \
  CMD wget -q -O - http://127.0.0.1:8080/health | grep -q '"status":"ok"' || exit 1
ENTRYPOINT ["/clinical-api"]
