# Use Controller Counters and Show Unattributed Traffic

The monitor uses Mihomo's official External Controller as its only traffic
source and treats global counters as the total-traffic authority. Connection
snapshots are inherently lossy for short-lived traffic, so the product exposes
Residual traffic and Gap-recovered traffic instead of pretending every byte can
be assigned to an App or Host. Packet capture and traffic interception were
rejected because they add privilege, platform, privacy, and correctness costs
that conflict with a local read-only observer.
