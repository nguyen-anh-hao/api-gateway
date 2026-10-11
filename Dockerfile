FROM golang:1.27 AS build
WORKDIR /src
COPY go.mod go.sum ./
COPY cmd ./cmd
COPY internal ./internal
RUN go build -o /bin/gateway ./cmd

FROM debian:bookworm-slim
COPY --from=build /bin/gateway /usr/local/bin/gateway
EXPOSE 8080
ENTRYPOINT ["gateway"]
