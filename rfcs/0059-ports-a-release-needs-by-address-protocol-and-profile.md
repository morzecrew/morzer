# RFC 0059 — Ports a release needs, by address, protocol and profile

- **Status:** 📝 Draft — proposed 2026-11-15. Decisions 5 and 9 are `OPEN`
  for the author. Nothing is built.
- **Scope:** Lets a `requirements.ports` entry say what a release really
  publishes: on which host address, over TCP or UDP, and under which
  profile. The entry is written as Compose writes a published port,
  `[address:]port[/protocol]`, and stays templated. `network.ports` then
  binds what the entry names. `machine.ports` counts two installations as
  colliding only when their protocol, port and addresses overlap. **Contract
  changes:**
  - the grammar of a `requirements.ports` entry, with a manager floor;
  - `.Profiles` and `has` in the manifest's template context, which RFC 0007
    decision 6 kept to `.Parameters`.

  No installation-state, document or hook ABI change. **Not covered:** port
  ranges, SCTP, host names as addresses, a check on a running deployment,
  and reading the ports from the Compose file.
- **Related:**
  - [`internal/domain/manifest_template.go`](../internal/domain/manifest_template.go):
    the template context and `ResolvePorts`;
  - [`internal/domain/manifest.go`](../internal/domain/manifest.go): `PortSpec`
    and the validation of `requirements.ports`;
  - [`internal/lifecycle/preflight/preflight.go`](../internal/lifecycle/preflight/preflight.go):
    `Ports` and `portFree`;
  - [`internal/lifecycle/ops/doctor_machine.go`](../internal/lifecycle/ops/doctor_machine.go):
    `checkMachinePorts` and `declaredPorts`;
  - [RFC 0007](0007-operator-parameters.md) decisions 5 and 6;
  - [RFC 0020](0020-several-installations-on-one-machine.md) decision 5e and
    §13;
  - [RFC 0053](0053-several-profiles-per-installation.md) §8 and §10;
  - [RFC 0055](0055-macos-as-a-host-for-an-installation.md), for the floor
    pattern this follows.
- **Discussion:** issues #366 and #367.

---

## 1. Summary

A `requirements.ports` entry can name an address and a protocol, in the
spelling a Compose file uses: `"{{ .Parameters.address }}:443"`,
`"8447/udp"`, `"[2001:db8::1]:443/tcp"`. A bare port keeps today's meaning:
TCP on every address. An entry can also depend on the profiles an installation
selects, through `.Profiles` and `has`. A templated entry that renders empty is
no requirement. `network.ports` binds a TCP listener or a UDP socket on the
entry's address. `machine.ports` keys a collision on protocol and port, and
reports it only when the two addresses overlap. A release that uses any of
this needs a manager floor, so an older manager refuses it at
`release verify`, not halfway through an apply.

## 2. Motivation

Three real cases, all from dotdash, that the field cannot describe today.

**A second address on a shared machine (#366).** A machine runs its own web
server on its first public address, for example Caddy with
`default_bind 203.0.113.10`, on 443. A bundle publishes
`${ADDRESS}:443:443/tcp` on the second address, 203.0.113.11. The two never
collide. Preflight still fails with `port 443 already in use`, and the check is
fatal, so the release never installs. `portFree` listens on `":443"`
([`preflight.go:392`](../internal/lifecycle/preflight/preflight.go)). On
Linux that is a dual-stack wildcard bind, which fails while any address holds
443. `machine.ports` has the same blind spot: two installations publishing 443
on different addresses are reported as colliding.

**UDP (#367).** `network.ports` only ever listens on TCP. dotdash's `xdns`
profile publishes UDP/53 and its `mkcp` profile a UDP port. A busy UDP port is
found by Compose at `up`, inside the apply, after the old containers have
stopped. The preflight check exists to refuse that before anything stops.

**Ports only a profile publishes (#367).** The field is rendered with
`.Parameters` alone (`manifest_template.go:52`). The `xhttp-reality` profile
publishes a second TCP port, which the field cannot list without requiring it
free on every installation, whether it selects the profile or not. dotdash's
server manifest carries a comment saying so. RFC 0053 §8 named this as a
follow-up "when a second bundle asks", and #367 is that ask.

The address case also interacts with #368: a `listen` parameter that defaults
to empty is how a Compose file says "every address, IPv4 and IPv6". The entry
`"{{ .Parameters.listen }}:443"` then renders `":443"`, and has to mean the
same thing the Compose file's `${LISTEN}:443:443` does.

## 3. Current state

Verified against `main` at faf30f7e.

- **The type.** `Requirements.Ports` is `[]PortSpec`, and `PortSpec` is a
  string that also accepts a YAML integer (`manifest.go:681-700`). The JSON
  schema types each item as a string, "a port number, or a
  `{{ .Parameters.<name> }}` reference".
- **Validation at load** (`manifest.go:1831-1851`). A literal entry must be an
  integer from 1 to 65535. A templated one is parsed for syntax only, since its
  values are an installation's. An empty entry is refused.
- **Resolution.** `ResolvePorts(params) ([]int, error)`
  (`manifest_template.go:78`) renders each entry with
  `templateContext{Parameters: p}` and `missingkey=error`, then requires an
  integer port. The context holds `.Parameters` and nothing else, by RFC 0007
  decision 6 (`manifest_template.go:10-23`). There is no function map.
  `health.checks[].url` is the only other templated field (RFC 0007 decision
  5), resolved by `ResolveHealthChecks` with the same context.
- **The preflight check.** `preflight.Ports([]int)` (`preflight.go:365`) is
  fatal. It reports every port that `net.Listen("tcp", ":"+port)` cannot bind
  as `port N already in use`. `apply` adds it only when the project is not
  already running (`apply.go:303-316`), so the product's own listeners are not
  reported.
- **The machine check.** `declaredPorts` (`doctor_machine.go:165`) resolves
  each installation's ports against its own parameters, into
  `map[int][]string`. `checkMachinePorts` warns for any port that more than
  one product claims. RFC 0020 decision 5e asked for a key of host IP, port and
  protocol. Its §13 recorded that the declared port number shipped instead,
  because nothing an installation declares has an address or a protocol, and
  that "5e is the decision to implement" once `requirements.ports` grows an
  address.
- **The configuration templates** see `.Profiles`, the effective selection in
  layering order and never nil (`gotemplate/render.go:169-171`). They also
  have `has`, in sprig's argument order: `has "xdns" .Profiles`
  (`render.go:302`).
- **The floor pattern.** A manifest feature an older manager would refuse
  carries a `<Feature>MinManagerVersion` constant. Validation then refuses a
  manifest that uses the feature with a lower `min_manager_version`, as
  `PlatformsMinManagerVersion` does (`manifest.go:871-875`, `:1803-1808`).

## 4. Goals / Non-goals

**Goals**

- An entry that names an address is checked on that address alone, and
  collides on the machine only with a claim whose address overlaps.
- A UDP entry is checked with a UDP socket, and never collides with TCP.
- An entry can depend on the installation's profiles, and one that renders
  empty requires nothing.
- A manifest written for today's grammar means exactly what it means today.
- An older manager refuses a manifest written in the new grammar, naming the
  floor, at `release verify`.

**Non-goals**

- Deriving the ports from the Compose file. `docker compose config` per
  installation is a subprocess per row in a check that must work when the
  daemon is down. RFC 0020 §13 rejected it for `machine.ports`, and the same
  holds here. The vendor declares, as today.
- Checking what is published against what is declared. A Compose file that
  publishes a port the manifest leaves out is unchanged: Compose reports it
  at `up`.

## 5. Design

### 5.1 The entry

```text
entry    = [ address ":" ] port [ "/" protocol ]
address  = IPv4 literal | "[" IPv6 literal "]" | ""      ; "" = every address
port     = 1-65535, decimal
protocol = "tcp" | "udp"                                 ; default "tcp"
```

The same spelling as a Compose short-syntax published port, with the
container side left out. Examples: `"443"`, `"8447/udp"`,
`"203.0.113.11:443"`, `":443"`, `"[2001:db8::1]:443/tcp"`. Refused, as a
validation error naming the entry: a port range (`8000-8010`), `sctp`, a host
name, an IPv6 literal without brackets, a zone (`fe80::1%eth0`), and anything
left over.

An empty address means every address, as Compose reads `":443:443"`. So
`"{{ .Parameters.listen }}:443"` with `listen` empty is the bare `"443"`
(decision 2).

Resolved, an entry is:

```go
// PortRequirement is one port a release needs, resolved.
type PortRequirement struct {
	// Address is the zero netip.Addr for every address.
	Address  netip.Addr
	Port     uint16
	Protocol PortProtocol // "tcp" or "udp"
}

// String is the entry's canonical spelling, used in every message:
// "443/tcp", "203.0.113.11:443/tcp", "[2001:db8::1]:53/udp".
func (r PortRequirement) String() string

func (m *Manifest) ResolvePorts(params Parameters, profiles []string) ([]PortRequirement, error)
```

Validation at load keeps its split. A literal entry is parsed by the grammar.
A templated one is still checked for template syntax only.

### 5.2 Profiles in the template

The manifest's template context gains `.Profiles`, the installation's
selection as configuration templates see it: in layering order, never nil. It
also gains one function, `has`, the same function as in the configuration
templates, in the same argument order:

```yaml
requirements:
  ports:
    - "80"
    - "{{ .Parameters.https_port }}"
    - '{{ if has "mkcp" .Profiles }}{{ .Parameters.kcp_port }}/udp{{ end }}'
    - '{{ if has "xdns" .Profiles }}53/udp{{ end }}'
```

A templated entry that renders empty, after trimming, requires nothing. A
literal empty entry stays a validation error: written by hand, it can only be
a mistake.

Which fields get `.Profiles` is decision 5. The recommendation is both of
RFC 0007 decision 5's fields, `requirements.ports` and `health.checks[].url`.
With one context, a profile that moves the application's port can move its
health URL with it, and `.Profiles` means the same thing wherever a manifest
can write it. The rest of RFC 0007 decision 6 stands: no paths, no secrets, no
installation. A profile name is not a secret, and it is already in
`status --json`.

Callers pass the installation's effective selection. `apply`'s preflight and
health checks pass the one being applied. `machine.ports` passes each
installation's own. With no installation, as in `release verify`, the
selection is empty.

### 5.3 `network.ports`

`preflight.Ports([]PortRequirement)` binds what each entry names, and closes
it at once:

| Entry | Probe |
| --- | --- |
| TCP, every address | `net.Listen("tcp", ":443")`, as today |
| TCP, an IPv4 address, `0.0.0.0` included | `net.Listen("tcp4", "203.0.113.11:443")` |
| TCP, an IPv6 address, `::` included | `net.Listen("tcp6", "[2001:db8::1]:443")` |
| UDP, every address | `net.ListenPacket("udp", ":53")` |
| UDP, one address | `udp4` or `udp6`, by the address's family |

An entry with an address binds that address's family alone. `tcp6` on
`[::]` sets `IPV6_V6ONLY`, so an IPv4 listener on 443 does not make
`[::]:443` busy. That matches Docker 27 and later, which publish `::` on
IPv6 alone (§9). Plain `tcp` on `[::]` would bind both families, and refuse
the entry over a listener it never meets.

The probe's errors are told apart (decision 3):

- `EADDRINUSE` is `port 203.0.113.11:443/tcp already in use`, fatal, with
  today's hint;
- `EADDRNOTAVAIL` is `203.0.113.11 is not an address of this machine`, fatal,
  with a hint naming the parameter if the entry interpolates one. Compose
  fails the same way at `up`, after the old containers have stopped;
- anything else, such as `EACCES` for a port below 1024 as a non-root user, is
  reported with its own error, still fatal, and never as "in use".

Not changed: the check runs only when the project is not running
(`apply.go:303`), and it stays fatal.

### 5.4 `machine.ports`

The claims are keyed by protocol and port. Two claims under one key collide
when their addresses overlap (decision 4):

- either one is every address;
- they are the same address;
- one is `0.0.0.0` and the other an IPv4 address;
- one is `::` and the other an IPv6 address.

The warning names the claims as written: `443/tcp (dotdash on
203.0.113.11, web on every address)`. A claim of 443/tcp and one of 443/udp
never collide. This is RFC 0020 decision 5e, implemented now that an entry
carries what it names.

### 5.5 The floor

`PortsMinManagerVersion` is the manager release that ships this (decision 9).
Validation requires it when the manifest:

- has a literal entry with an address or a protocol;
- has a templated entry that refers to `.Profiles` or calls `has`, in
  `requirements.ports` or, per decision 5, in `health.checks[].url`;
- has a templated `requirements.ports` entry with `:`, `/` or `[` outside
  its actions, such as `"{{ .Parameters.address }}:443"`.

A templated entry whose text outside its actions is digits and whitespace
needs no floor, since it resolves today as it will then: a lone
`{{ .Parameters.<name> }}`, or `8{{ .Parameters.suffix }}`. A string
parameter that holds `1.2.3.4:443` gets past this rule. An older manager
refuses it with `is not a port number` wherever it resolves the ports: at
preflight while the project is not running, and in `doctor`, whose
`machine.ports` names the installation as one it could not read. On a
running project, preflight skips the port check (`apply.go:303`), so the
apply converges with that entry unchecked, as it would with any port
today (§9).

An older manager fails closed on every new form even without the floor:
- a literal address or protocol is refused at load;
- `.Profiles` is `can't evaluate field Profiles`;
- `has` is `function "has" not defined`.

The floor only turns those failures into one message that names the version.

### 5.6 Alternatives considered

- **New keys instead of a grammar:** `{port, address, protocol, profiles}`
  maps in place of strings. Each part is more explicit, but the parts the
  vendor already writes in Compose get a second spelling. It also needs a
  schema `oneOf` beside today's strings, and a profile list is a second way to
  say what `has` already says. The Compose spelling costs the vendor nothing
  to learn.
- **A per-profile `ports` list under `runtimes.<runtime>.profiles`.** This
  ties a port to a profile without templates. It is the narrower answer, but
  it cannot express a port that a parameter turns on, and RFC 0053 §8 already
  pointed at `.Profiles` and `has`.
- **Probing with Docker itself**, publishing a throwaway container on the
  entry. This checks exactly what Compose will do. It also needs the daemon
  and an image, and takes seconds per entry in a check that runs on every
  apply.

## 6. Tests

- **The grammar**, as a table test over every row in §5.1: the accepted
  forms, their canonical `String()`, and each refused form with its message.
  This includes `":443"` equal to `"443"`, and the YAML integer still
  accepted.
- **Resolution**: `.Profiles` and `has` in an entry, the selection in layering
  order, an entry that renders empty dropped, and a literal empty one refused.
  A health URL that uses `.Profiles` too, if decision 5 rules both fields.
- **The probe**, with real sockets on loopback:
  - holding `127.0.0.1:P/tcp` makes `127.0.0.2:P` free, but `P` on every
    address busy (127.0.0.0/8 is all local on Linux);
  - the same for UDP;
  - a TCP hold never makes the UDP entry busy;
  - an address the machine lacks (TEST-NET 192.0.2.1) is reported as not an
    address of this machine, never as in use;
  - an IPv4 hold on `P` leaves `[::]:P` free, where the host has IPv6.
- **The machine check**: a table of claim pairs over §5.4's rules, with each
  side's profiles, and two installations on different addresses reported
  clear.
- **The floor**: each form in §5.5 below the floor refused, a health URL
  alone using `.Profiles` included. A lone parameter reference and
  `8{{ .Parameters.suffix }}` are accepted, and a fixture manifest in today's
  grammar is unchanged.
- **Suite**: an installation whose profile adds a UDP port. Its apply is
  refused while a socket holds that port, and an installation without the
  profile is not.

## 7. Docs

- `reference/manifest.md` `requirements.ports`: the grammar, the empty
  address, `.Profiles` and `has`, the empty-render rule and the floor. Also
  the example from §5.2.
- `reference/manifest.md` templating section: the context is `.Parameters` and
  `.Profiles` (per decision 5), with `has`.
- The doctor checks reference: `network.ports` messages, the "not an address
  of this machine" refusal, and `machine.ports`' overlap rule.
- The manifest schema's description of a `requirements.ports` item
  (`internal/schema/schema.go`, regenerated into `schemas/`). It says "a
  port number, or a `{{ .Parameters.<name> }}` reference" today, and gains
  the grammar and its examples, for editors that read the schema.
- An authoring note on sharing a machine with a web server the operator
  already runs, with both setups: a second address, or 127.0.0.1 behind their
  TLS passthrough.
- The changelog, as an Added entry with the floor.
- On acceptance, RFC 0007 decision 6 is marked superseded in part by
  decision 5 here, and RFC 0020 decision 5e marked implemented.

## 8. Out of scope

- **Port ranges** (`8000-8010`). Nothing asks for them, and a range makes a
  collision a range overlap. They can come later with their own rows.
- **SCTP.** Compose accepts it, but no bundle uses it.
- **Host names as addresses.** Preflight would then depend on DNS, at the
  moment it decides whether to stop the old containers.
- **A check on a running deployment.** A `config set profile` that adds a
  port, applied to a running project, skips `network.ports` as today
  (`apply.go:303`). Compose reports a busy port at `up`. Checking would mean
  telling the product's own listeners apart from a stranger's, which is a
  design of its own.
- **macOS.** On Docker Desktop a published port is bound by the Desktop
  process, not inside the VM. Whether the same probe agrees there is for
  RFC 0055's acceptance run, not this RFC.

## 9. Risks

- **The probe and Docker disagree.** A bind that succeeds is not proof that
  Docker's own bind will. The known case is UDP/53 on Ubuntu, where
  systemd-resolved's stub listens on 127.0.0.53:53. Whether a wildcard UDP
  bind, and Docker's publish of 53/udp, succeed beside it depends on socket
  options, and this has not been measured (§10). Mitigation: measure before
  building, and match the probe to Docker's result.
- **A string parameter that holds a whole entry** gets past the floor rule
  (§5.5). An older manager refuses it only where it resolves the ports. On
  a running project the apply converges with that entry unchecked, and
  `doctor` names the installation.
  Accepted: the alternative is rendering every entry at load with invented
  values, which RFC 0007 kept out of load-time validation.
- **The `0.0.0.0` and `::` rules** follow Docker 27 and later, where `0.0.0.0`
  publishes IPv4 only. On an older engine `::` may also bind IPv4. The rule
  can then miss a real collision, and it is only a warning. Accepted.

## 10. Unresolved questions

- **UDP/53 beside systemd-resolved** (§9). On Ubuntu 24.04 with the stub
  listener on, measure whether `net.ListenPacket("udp", ":53")` succeeds and
  whether Compose can publish `53:53/udp`. Settled by that measurement before
  §5.3 is built. If the two disagree, the probe follows Docker.
- Decisions 5 and 9 below, for the author.

## 11. Decisions

| # | Grade | Decision |
| --- | --- | --- |
| 1 | `ASSUMED` | An entry is `[address:]port[/protocol]`, Compose's short-syntax spelling without the container side. The address is an IP literal, IPv6 in brackets. The protocol is `tcp` or `udp`, `tcp` when absent. Ranges, `sctp`, host names, zones and unbracketed IPv6 are validation errors. |
| 2 | `ASSUMED` | An empty address and no address both mean every address. So `"{{ .Parameters.listen }}:443"` with `listen` empty is `"443"`, as the Compose file's `${LISTEN}:443:443` is every address (#368). |
| 3 | `ASSUMED` | `network.ports` binds the entry's protocol on the entry's address, in that address's family alone (`tcp6` on `[::]`), and closes at once. `EADDRINUSE` is "already in use". `EADDRNOTAVAIL` is "not an address of this machine". Any other error is reported as itself. All three are fatal, as the check is today. |
| 4 | `ASSUMED` | `machine.ports` keys claims by protocol and port. Two claims collide when either is every address, they are the same address, or one is `0.0.0.0` beside IPv4 or `::` beside IPv6. This implements RFC 0020 decision 5e. |
| 5 | `OPEN` | Which fields see `.Profiles` and `has`. **Recommended:** both fields RFC 0007 decision 5 lets interpolate, `requirements.ports` and `health.checks[].url`, with one context, so a profile that moves a port moves its health URL with it. This supersedes RFC 0007 decision 6 in part: the context is `.Parameters` and `.Profiles`, and still no paths, secrets or installation. Alternative: `requirements.ports` alone. That is a smaller change, but `.Profiles` would then work in one manifest field and fail in the other, as an unknown field at resolution time. |
| 6 | `ASSUMED` | `.Profiles` is the installation's effective selection in layering order, never nil, as configuration templates see it. Each caller passes the installation it checks. With none, as in `release verify`, it is empty. |
| 7 | `ASSUMED` | A templated entry that renders empty after trimming requires nothing. A literal empty entry is still a validation error. |
| 8 | `ASSUMED` | `PortsMinManagerVersion` is required when a port entry uses an address or a protocol literally, when either templated field refers to `.Profiles` or `has`, or when a templated port entry has `:`, `/` or `[` outside its actions. An entry whose text outside its actions is digits needs no floor, a lone `{{ .Parameters.<name> }}` or `8{{ .Parameters.suffix }}`, since it resolves today as it will then. The floor names the version at `release verify`. Older managers refuse every new form where they resolve it anyway. |
| 9 | `OPEN` | The floor's value. **Recommended:** 0.18.0 if this ships before 0.18.0 is cut, the minor that already carries `PlatformsMinManagerVersion`, and the next minor otherwise. |
| 10 | `ASSUMED` | The probe is a socket bind, never Docker. If §10's measurement shows that the two disagree for UDP/53, the probe is changed to agree with Docker, and the change is recorded here. |

## 12. Phasing

One PR, after the author rules on decisions 5 and 9 and §10's measurement is
in:

- the grammar, `PortRequirement` and `ResolvePorts` with profiles (§5.1, §5.2);
- the probe (§5.3) and the machine check (§5.4);
- the floor and its validation (§5.5);
- the docs in §7 and the changelog.

The grammar without `.Profiles` (#366 alone) could land first, if the author
wants dotdash's second-address setup unblocked before profile-aware ports.
