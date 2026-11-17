---
title: Sharing a machine with a web server
icon: lucide/network
summary: Letting a release run on a machine whose operator already serves 443, on a second address or behind their TLS passthrough
---

# Sharing a machine with a web server

An operator who already runs a web server on 443 cannot install a release that
needs 443 on every address: `apply` refuses with `port 443/tcp already in
use`. Give them one parameter that says where your release listens, and they
can choose between two setups. How `requirements.ports` reads an address is in
the [manifest reference](../reference/manifest.md#ports).

## Make the address a parameter

Declare it, empty by default, which is every address:

```yaml
parameters:
  listen:
    type: string
    default: ""
    description: The host address to publish on; empty is every address.

requirements:
  ports: ["{{ .Parameters.listen }}:443"]

compatibility:
  min_manager_version: "0.18.0"
```

Publish on it in the Compose file, so the check and the deployment name the
same address:

```yaml
services:
  app:
    ports:
      - "${DEMO_PARAM_LISTEN}:443:443"
```

An empty `listen` makes the entry `":443"`, which is the bare `443`, and
Compose's `":443:443"` publishes on every address too. An installation that sets
nothing behaves as before.

## On a second address

On a machine with two public addresses, the operator's web server keeps the
first, `203.0.113.10` say, and your release takes the second:

```sh
morzer config set listen=203.0.113.11
morzer apply
```

`network.ports` then binds 443 on `203.0.113.11` alone, which is free beside a
web server bound to `203.0.113.10`. One that listens on every address holds
both, and the check still refuses; the operator binds it to its own address
first, as Caddy's `default_bind` or nginx's `listen 203.0.113.10:443` does. An
address the machine does not have is refused as `203.0.113.11 is not an
address of this machine`, naming `listen`.

## Behind the operator's TLS passthrough

On a machine with one address, the operator's web server keeps 443 and passes
your release's connections through by the name the client asks for, without
ending TLS. Your release listens on the loopback address, on a port of its
own, so give the port a parameter too:

```yaml
requirements:
  ports: ["{{ .Parameters.listen }}:{{ .Parameters.https_port }}"]
```

```yaml
services:
  app:
    ports:
      - "${DEMO_PARAM_LISTEN}:${DEMO_PARAM_HTTPS_PORT}:443"
```

```sh
morzer config set listen=127.0.0.1 https_port=8443
morzer apply
```

The operator then routes your name to `127.0.0.1:8443` in their passthrough:
nginx's `stream` module with `ssl_preread`, or HAProxy in `mode tcp`. Your
release still terminates TLS itself, and nothing outside the machine reaches
8443.

## Check it

`release verify` refuses the manifest below the `0.18.0` floor. Install it
twice on one machine, `listen` set to two different addresses, and run `morzer
doctor`: `machine.ports` reports the two as colliding with nobody. With
`listen` empty on either, it reports 443/tcp, since every address meets every
other.
