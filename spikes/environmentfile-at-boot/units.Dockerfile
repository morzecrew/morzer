FROM morzer-envfile-spike:venue

# The manager's render step, standing in for `apply --startup`: it writes the
# parameter file into the tmpfs that systemd mounts at /run.
RUN printf '%s\n' \
  '[Unit]' \
  'Description=render parameters (stands in for apply --startup)' \
  '' \
  '[Service]' \
  'Type=oneshot' \
  'RemainAfterExit=yes' \
  'ExecStart=/bin/sh -c "mkdir -p /run/demo; echo DEMO_PARAM_HTTP_PORT=18080 > /run/demo/params.env"' \
  '' \
  '[Install]' \
  'WantedBy=multi-user.target' \
  > /etc/systemd/system/demo-render.service

# A: no ordering, no dash -- the naive Quadlet unit.
RUN printf '%s\n' \
  '[Unit]' 'Description=product A (unordered, no dash)' '' \
  '[Service]' 'Type=oneshot' 'RemainAfterExit=yes' \
  'EnvironmentFile=/run/demo/params.env' \
  'ExecStart=/bin/sh -c "echo A_PORT=[$DEMO_PARAM_HTTP_PORT]"' '' \
  '[Install]' 'WantedBy=multi-user.target' \
  > /etc/systemd/system/demo-a.service

# B: no ordering, with the dash -- the silence §4.3 fears.
RUN printf '%s\n' \
  '[Unit]' 'Description=product B (unordered, dash)' '' \
  '[Service]' 'Type=oneshot' 'RemainAfterExit=yes' \
  'EnvironmentFile=-/run/demo/params.env' \
  'ExecStart=/bin/sh -c "echo B_PORT=[$DEMO_PARAM_HTTP_PORT]"' '' \
  '[Install]' 'WantedBy=multi-user.target' \
  > /etc/systemd/system/demo-b.service

# C: ordered behind the render, no dash.
RUN printf '%s\n' \
  '[Unit]' 'Description=product C (ordered, no dash)' \
  'After=demo-render.service' 'Requires=demo-render.service' '' \
  '[Service]' 'Type=oneshot' 'RemainAfterExit=yes' \
  'EnvironmentFile=/run/demo/params.env' \
  'ExecStart=/bin/sh -c "echo C_PORT=[$DEMO_PARAM_HTTP_PORT]"' '' \
  '[Install]' 'WantedBy=multi-user.target' \
  > /etc/systemd/system/demo-c.service

RUN systemctl enable demo-render.service demo-a.service demo-b.service demo-c.service
