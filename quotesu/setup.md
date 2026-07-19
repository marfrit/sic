# quotesu — test target for sicd

Incus container on `dcw2` (192.168.88.114), created as the target host for
sicd development and quoting-hell demonstrations.

## Host facts

| Property | Value |
|---|---|
| Host machine | `dcw2` (192.168.88.114, aarch64, Debian 13) |
| Container IP | 192.168.88.196/24 |
| Network | `br0` (unmanaged bridge) |
| Storage | `usb` pool (btrfs, /mnt/usb-noether) |
| OS | Debian 13 "trixie" (arm64) |
| Incus version | 6.0.4 |
| SSH user | `mfritsche` (key auth + passwordless sudo) |

## Setup steps

```sh
# 1. Create container
incus init debian-13 quotesu --storage usb --network br0
incus start quotesu

# 2. Install SSH + user
incus exec quotesu -- apt-get update -qq
incus exec quotesu -- apt-get install -y -qq openssh-server sudo python3
incus exec quotesu -- adduser --disabled-password --gecos "" mfritsche
incus file push ~/.ssh/authorized_keys quotesu/home/mfritsche/.ssh/authorized_keys
incus exec quotesu -- chown -R mfritsche:mfritsche ~mfritsche/.ssh
incus exec quotesu -- chmod 700 ~mfritsche/.ssh
incus exec quotesu -- chmod 600 ~mfritsche/.ssh/authorized_keys
incus exec quotesu -- adduser mfritsche sudo
echo "mfritsche ALL=(ALL) NOPASSWD:ALL" | \
  incus exec quotesu -- tee -a /etc/sudoers.d/mfritsche
incus exec quotesu -- systemctl restart ssh

# 3. Deploy sicd
incus exec quotesu -- mkdir -p /opt/sicd
# Copy ../gateway/sicd to the container, then:
incus exec quotesu -- chmod 755 /usr/local/bin/sicd
incus exec quotesu -- ln -sf /usr/local/bin/sicd /usr/local/bin/sicd
```

## Verification

```sh
# Test the gateway
printf '4:exec,2:id,0:,' | ssh quotesu sicd
# → uid=1000(mfritsche) gid=1000(...
```

## DNS note

dcw2 had a broken `/etc/resolv.conf` (NetworkManager header, no nameservers).
Fixed by setting:
```
nameserver 1.1.1.1
nameserver 192.168.88.1
```
This was needed for `incus image copy images:debian/13 local:...` to resolve
the image server hostname.
