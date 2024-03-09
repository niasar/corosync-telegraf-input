# Telegraf corosync external plugin

This plugins monitors corosync cluster node status.

### Requirements

This plugin only works witch corosync versions >=3

### Building

Building require go version >=1.22

`go build -o corosync-plugin ./cmd/main.go`

### Usage

Create configuration file for plugin

```toml
[[inputs.corosync]]
  ## Set to false if you running telegraf as root
  # use_sudo = true

```

If you running telegraf as non-root, then you need to add sudoers rules for its user to allow execution of corosync-quorumtool and corosync-cfgtool:

```
Cmnd_Alias    QUORUMTOOL = /usr/sbin/corosync-quorumtool
Cmnd_Alias    CFGTOOL = /usr/sbin/corosync-cfgtool

telegraf ALL=(ALL) NOPASSWD: QUORUMTOOL, CFGTOOL

Defaults!CFGTOOL, QORUMTOOL !syslog, !pam_session, !logfile

```

Add execd input for telegraf

```toml
[[inputs.execd]]
command = ["/path/to/plugin/binary", "-config", "/path/to/plugin/config"]
signal = "none"
```

### Collected data

corosync_quorum:

* Fields:
  * Ring ID (string)
  * Total nodes (uint)
  * Total votes (uint)
  * Expected votes (uint)
  * Highest expected (uint)
  * Quorum (uint)
  * Quorate flag (bool)
* Tags:
  * Node ID

corosync_rings:

* Fields:
  * Active count (uint)
  * Connected count (uint)
  * Enabled count (uint)
  * Unknown count (uint)
  * Undefined count (any other status) (uint)
* Tags:
  * Ring ID

### Sample output

```
corosync_quorum,node_id=2 total_nodes=5i,ring_id="1.2a8",total_votes=5i,expected_votes=5i,highest_expected=5i,quorum=3i,is_quorate=true 1710019927989283282
corosync_rings,ring_id=0 active=4i,connected=0i,enabled=0i,unknown=0i,undefined=0i,total=4i 1710019927989294042
```
