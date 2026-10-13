FROM golang:1.25-alpine AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY *.go ./
ARG BUILD_VERSION=dev
RUN CGO_ENABLED=0 go build -trimpath -ldflags "-s -w -X main.version=${BUILD_VERSION}" -o /slimhuys-eebus .

FROM alpine:3.22
RUN apk add --no-cache ca-certificates tzdata
COPY --from=build /slimhuys-eebus /usr/bin/slimhuys-eebus
CMD ["/usr/bin/slimhuys-eebus"]
