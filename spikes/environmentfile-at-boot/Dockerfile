# Pinned by digest, because the spike's whole claim is that its numbers can be
# re-run. A floating tag re-runs a different experiment and reports it as the
# same one -- which is worse than not keeping the venue at all, since the second
# answer arrives wearing the first one's authority.
#
# archlinux:latest as of 2026-08-19. Repin deliberately, and re-record the
# systemd version below when you do.
FROM archlinux@sha256:4bf33b21a715aac0b48ce6e9eaed4782a898eae96f88f5da3635572129c2584a

# The measurement in RFC 0023 §12 item 4 was taken against systemd 261
# (261.2-1-arch). `pacman -Sy` resolves against a moving mirror, so this asserts
# the major version rather than trusting it: a venue running something else is a
# venue answering a different question, and it says so instead of reporting a
# number that looks comparable.
RUN pacman -Sy --noconfirm systemd && \
    rm -rf /var/cache/pacman/pkg/* && \
    v="$(systemctl --version | head -1 | awk '{print $2}')" && \
    if [ "$v" != "261" ]; then \
        echo "venue drift: systemd $v, measurement was taken on 261" >&2; \
        echo "re-run the spike, record the new numbers, and update this check" >&2; \
        exit 1; \
    fi

# The boot transaction is the measurement, so nothing here may pre-create the
# render directory: /run is a tmpfs that comes up empty.
STOPSIGNAL SIGRTMIN+3
CMD ["/sbin/init"]
