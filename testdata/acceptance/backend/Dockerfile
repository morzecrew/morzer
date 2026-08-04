# The backend tier: serves its own health endpoint on its own port, and is the
# only tier holding the database credential.
FROM alpine:3.21

RUN apk add --no-cache busybox-extras

RUN mkdir -p /www/health /www/api && \
    printf 'backend\n' > /www/health/ready && \
    printf 'backend\n' > /www/api/version

EXPOSE 8080
CMD ["httpd", "-f", "-p", "8080", "-h", "/www"]

STOPSIGNAL SIGKILL
