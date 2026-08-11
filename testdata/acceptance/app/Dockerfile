# The application stub: serves the health endpoint the manifest checks.
FROM alpine:3.21

# httpd is a busybox applet, but alpine's base busybox is built without it --
# it lives in busybox-extras. Found by the acceptance run failing with
# `exec: "httpd": executable file not found in $PATH`, which is the sort of
# thing only running the image tells you.
RUN apk add --no-cache busybox-extras

# /health/ready is the path manifest.yaml's http check requests.
RUN mkdir -p /www/health && printf 'ok\n' > /www/health/ready

EXPOSE 18080

# -vv logs every request to stderr, which is what makes this stub produce
# container logs at all. Without it the acceptance run's `morzer logs` stage
# would assert against an empty stream and pass whether or not the manager can
# reach the runtime's logs -- and the health check already generates the
# traffic, so the lines cost nothing to produce.
CMD ["httpd", "-f", "-vv", "-p", "18080", "-h", "/www"]

# Nothing to flush: this is a stub. Without it, `sleep`/`httpd` as PID 1
# ignore SIGTERM (PID 1 has no default signal actions) and every stop waits out
# the full Compose timeout.
STOPSIGNAL SIGKILL
