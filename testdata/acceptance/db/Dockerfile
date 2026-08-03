# The database stub. It stores nothing: the schema marker the migrate and
# check-db hooks care about lives in the data directory on the host, which is
# what the backup and restore hooks archive.
FROM alpine:3.21

CMD ["sleep", "infinity"]

# Nothing to flush: this is a stub. Without it, `sleep`/`httpd` as PID 1
# ignore SIGTERM (PID 1 has no default signal actions) and every stop waits out
# the full Compose timeout.
STOPSIGNAL SIGKILL
