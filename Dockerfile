FROM node:26.13.0-alpine AS ui
WORKDIR /src/web/ui
COPY web/ui/package*.json ./
RUN npm ci
COPY web/ui/ ./
RUN npm run check && npm run build

FROM golang:1.27.3-alpine AS builder
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
COPY --from=ui /src/web/ui/dist ./web/ui/dist
ARG BUILD_REVISION=unknown
ARG BUILD_STATE=unknown
RUN CGO_ENABLED=0 go build -mod=readonly -trimpath -buildvcs=false \
    -ldflags="-s -w -X main.buildRevision=${BUILD_REVISION} -X main.buildState=${BUILD_STATE}" \
    -o /fbhook ./cmd/fbhook

FROM alpine:3.24
RUN apk add --no-cache ca-certificates && addgroup -S fbhook && adduser -S -G fbhook fbhook
COPY --from=builder /fbhook /usr/local/bin/fbhook
USER fbhook
EXPOSE 8080
ENTRYPOINT ["/usr/local/bin/fbhook"]
CMD ["serve"]
