# syntax=docker/dockerfile:1
FROM golang:1.27-alpine AS build
WORKDIR /src
ENV CGO_ENABLED=0
COPY go.mod go.sum ./
RUN go mod download
COPY cmd ./cmd
COPY internal ./internal
COPY knowledge ./knowledge
COPY fixtures ./fixtures
RUN go test ./... && go build -trimpath -o /out/server ./cmd/server && go build -trimpath -o /out/crm ./cmd/crm

FROM alpine:3.23
RUN apk add --no-cache ca-certificates && mkdir /data && chown 65532:65532 /data
WORKDIR /app
COPY --from=build /out/server /out/crm /app/
COPY knowledge /app/knowledge
COPY fixtures /app/fixtures
USER 65532:65532
ENV LISTEN_ADDR=0.0.0.0:8086 KNOWLEDGE_PATH=/app/knowledge FIXTURES_PATH=/app/fixtures DATABASE_PATH=/data/support.sqlite EVALUATION_PATH=/reports/evaluation.json
EXPOSE 8086 8096
ENTRYPOINT ["/app/server"]
