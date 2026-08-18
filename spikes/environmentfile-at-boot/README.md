# Spike — an `EnvironmentFile` on tmpfs, at boot

Answers RFC 0023 §12 item 4. The deliverable is the measurement in that item and
decision 21; this directory is what produced it, kept so the numbers can be
re-run rather than believed.

## Why a venue at all

The question is about the state of a tmpfs *at boot*, and a host that is already
up cannot be asked. Item 4 asked for somewhere bootable on demand.

`systemd-nspawn` was the intended tool and needs root. What this uses instead is
a privileged container running systemd as PID 1: the same systemd, `/run` mounted
as a real tmpfs, and a real boot transaction — which is what the ordering
question needs. It boots in about a second, so the answer does not cost a reboot
per attempt, which was the objection to using a workstation.

**It is not bare metal.** It answers ordering and unit-start semantics. It cannot
answer anything about firmware, initrd, or real device mounts, and was not asked.

## Running it

    ./run.sh

Builds the venue, boots it, and prints the outcome of four units. Requires Docker
and nothing else. Removes the container when it finishes.

## What it sets up

A render unit standing in for `apply --startup`, which writes
`DEMO_PARAM_HTTP_PORT` into `/run/demo/params.env`, and three product units, all
`WantedBy=multi-user.target`:

| Unit | Ordering | Prefix |
|---|---|---|
| A | none | `EnvironmentFile=` |
| B | none | `EnvironmentFile=-` |
| C | `After=`+`Requires=` render | `EnvironmentFile=` |

## What it showed

A failed. B started, reported **success**, and ran with an empty parameter. C
worked. The timeline is the finding: B started *and finished* before the render
unit had written the file, which is why its success means nothing.
