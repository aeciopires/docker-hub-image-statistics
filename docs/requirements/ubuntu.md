# Ubuntu-specific requirements

Only what differs on Ubuntu 22.04, 24.04 and 26.04 (amd64). Everything else is
in [REQUIREMENTS.md](../../REQUIREMENTS.md).

<!-- TOC -->
- [System packages](#system-packages)
- [Docker Engine](#docker-engine)
- [Running Docker without sudo](#running-docker-without-sudo)
- [Notes per release](#notes-per-release)
<!-- TOC -->

## System packages

`make install-deps` runs (with `sudo`):

```bash
sudo apt-get update
sudo apt-get install -y --no-install-recommends ca-certificates curl git make python3 tar gzip
```

## Docker Engine

Install Docker Engine from Docker's official repository (the `docker.io` package
of Ubuntu lags behind and lacks the Compose v2/Buildx plugins):

```bash
sudo install -m 0755 -d /etc/apt/keyrings
sudo curl -fsSL https://download.docker.com/linux/ubuntu/gpg -o /etc/apt/keyrings/docker.asc
sudo chmod a+r /etc/apt/keyrings/docker.asc
echo "deb [arch=$(dpkg --print-architecture) signed-by=/etc/apt/keyrings/docker.asc] \
  https://download.docker.com/linux/ubuntu $(. /etc/os-release && echo "$VERSION_CODENAME") stable" \
  | sudo tee /etc/apt/sources.list.d/docker.list >/dev/null
sudo apt-get update
sudo apt-get install -y docker-ce docker-ce-cli containerd.io docker-buildx-plugin docker-compose-plugin
```

Reference: <https://docs.docker.com/engine/install/ubuntu/>.

## Running Docker without sudo

```bash
sudo usermod -aG docker "$USER"
newgrp docker      # or log out and in again
docker run --rm hello-world
```

Membership in the `docker` group is equivalent to root access on the host; on
shared machines consider [rootless Docker](https://docs.docker.com/engine/security/rootless/).

## Notes per release

| Release | Notes |
| --- | --- |
| 22.04 (jammy) | Ships Python 3.10 and GNU make 4.3: both are fine |
| 24.04 (noble) | No extra steps |
| 26.04 | No extra steps; if Docker's repository does not list the new codename yet, use the previous LTS codename in `docker.list` temporarily |
