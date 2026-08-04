# The frontend tier of the three-tier example: serves a page and a health
# endpoint, and holds no credentials at all.
FROM alpine:3.21

# httpd is a busybox applet, but alpine's base busybox is built without it --
# it lives in busybox-extras.
RUN apk add --no-cache busybox-extras

# The body identifies the tier, so the acceptance run can assert that the port
# it probed reached the *frontend* and not the backend. Two stubs answering an
# identical "ok" would let a swapped port mapping pass.
RUN mkdir -p /www/health && \
    printf 'frontend\n' > /www/health/ready && \
    printf 'frontend\n' > /www/index.html

EXPOSE 8080
CMD ["httpd", "-f", "-p", "8080", "-h", "/www"]

# Nothing to flush: this is a stub. Without it, httpd as PID 1 ignores SIGTERM
# and every stop waits out the full Compose timeout.
STOPSIGNAL SIGKILL
