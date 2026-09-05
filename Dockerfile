FROM golang:1.27 AS build
WORKDIR /src
COPY . .
RUN CGO_ENABLED=0 go build -trimpath -buildvcs=false -o /mini-cloud .

FROM debian:trixie-slim
RUN apt-get update && apt-get install -y --no-install-recommends ca-certificates python3 \
    && rm -rf /var/lib/apt/lists/* \
    && useradd --uid 10001 --create-home mini-cloud \
    && mkdir -p /apps && chown mini-cloud:mini-cloud /apps
COPY --from=build /mini-cloud /usr/local/bin/mini-cloud
USER 10001:10001
WORKDIR /home/mini-cloud
EXPOSE 9080
ENTRYPOINT ["/usr/local/bin/mini-cloud"]
CMD ["-config", "/etc/mini-cloud.json"]
