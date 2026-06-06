FROM golang:1.22-alpine AS builder

ARG SERVICE
WORKDIR /src
COPY go.mod ./
COPY internal ./internal
COPY origin ./origin
COPY edge ./edge
COPY router ./router
RUN CGO_ENABLED=0 go build -o /out/service ./${SERVICE}

FROM alpine:3.20

RUN adduser -D -g '' appuser
RUN mkdir -p /cache /data && chown -R appuser:appuser /cache /data
USER appuser
WORKDIR /app
COPY --from=builder /out/service /app/service
EXPOSE 8080 9000 9100
ENTRYPOINT ["/app/service"]
