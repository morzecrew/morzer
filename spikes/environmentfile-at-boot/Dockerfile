FROM archlinux:latest
RUN pacman -Sy --noconfirm systemd && \
    rm -rf /var/cache/pacman/pkg/*
# The boot transaction is the measurement, so nothing here may pre-create the
# render directory: /run is a tmpfs that systemd mounts itself at boot.
STOPSIGNAL SIGRTMIN+3
CMD ["/sbin/init"]
