FROM golang:1.25-alpine AS build
WORKDIR /src
RUN apk add --no-cache git
COPY go.mod go.sum ./
RUN go mod download
COPY . .
ARG BINARY=api
RUN CGO_ENABLED=0 GOOS=linux go build -trimpath -ldflags "-s -w" -o /out/app ./cmd/${BINARY}

FROM alpine:3.20
RUN apk add --no-cache ca-certificates curl
COPY --from=build /out/app /usr/local/bin/app
USER nobody
ENTRYPOINT ["/usr/local/bin/app"]
