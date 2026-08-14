# Mihomo Traffic Observability

This context describes locally observed network traffic reported by a single
Mihomo instance and the limits of attributing that traffic to applications and
destinations.

## Language

**App**:
The process identity reported by Mihomo for an observed connection. A missing
identity is represented by the canonical label `Unknown process`.
_Avoid_: Application, program, process name

**Host**:
The most specific destination identity known for a connection, selected from
the sniffed host, declared host, then destination IP. Domain names are
lowercase without a trailing dot.
_Avoid_: Destination, hostname, site

**Registrable domain**:
The Public Suffix List-derived domain under which a Host can normally be
registered. IP Hosts and names without a registrable domain have no such value.
_Avoid_: Root domain, base domain, top-level domain

**Observed traffic**:
Traffic whose growth is associated with an App and Host from a connection
snapshot.
_Avoid_: Attributed bytes, known traffic

**Residual traffic**:
Traffic seen in Mihomo's global counters during normal sampling that cannot be
associated with a connection after the reconciliation window.
_Avoid_: Lost traffic, sampling error, unknown bytes

**Gap-recovered traffic**:
Traffic recovered from global counter growth after collector disconnection,
when the missing interval cannot be attributed to Apps or Hosts.
_Avoid_: Residual traffic, interpolated traffic, backfilled traffic

**Collection gap**:
A bounded interval in which the monitor did not receive usable snapshots from
Mihomo.
_Avoid_: Outage, missing data, downtime
