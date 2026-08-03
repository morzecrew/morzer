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
CMD ["httpd", "-f", "-p", "18080", "-h", "/www"]

# Nothing to flush: this is a stub. Without it, `sleep`/`httpd` as PID 1
# ignore SIGTERM (PID 1 has no default signal actions) and every stop waits out
# the full Compose timeout.
STOPSIGNAL SIGKILL
