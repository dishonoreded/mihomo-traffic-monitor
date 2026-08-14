# Ship One Local Binary with SQLite

The product ships as one local executable containing the backend and web UI,
with SQLite as its durable minute-level store. This keeps installation and
operation simple for a single macOS user while preserving permanent queryable
history without a separate database or cloud service. The trade-off is a
deliberate single-machine, single-instance architecture rather than remote or
distributed collection.
