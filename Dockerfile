FROM haproxy:1.8-alpine

COPY auto-lb /
ENTRYPOINT ["/auto-lb"]
