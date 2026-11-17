# Requirements

Everything needed to build, test and run this project. Only the steps that
differ per operating system are in the OS-specific pages:
[Ubuntu](docs/requirements/ubuntu.md) · [macOS](docs/requirements/macos.md).

<!-- TOC -->
- [Supported platforms](#supported-platforms)
- [Overview](#overview)
- [1. Docker and Docker Compose (installed separately)](#1-docker-and-docker-compose-installed-separately)
- [2. Automatic installation](#2-automatic-installation)
- [3. Toolchain managed by mise](#3-toolchain-managed-by-mise)
- [4. Verify](#4-verify)
- [Hardware and network](#hardware-and-network)
- [Updating the tools](#updating-the-tools)
- [Troubleshooting](#troubleshooting)
<!-- TOC -->

## Supported platforms

| Operating system | Architectures |
| --- | --- |
| Ubuntu 22.04 LTS, 24.04 LTS, 26.04 LTS | amd64 |
| macOS (current releases) | amd64 (Intel), arm64 (Apple silicon) |

Other Linux distributions usually work but are not tested.

## Overview

| Kind | Tools | Installed by |
| --- | --- | --- |
| System tools | `git`, `make`, `curl`, `python3` | `make install-deps` (apt on Ubuntu, Xcode CLT on macOS) |
| Container runtime | Docker Engine 28+ (Ubuntu) or **Colima** + Docker CLI (macOS), Docker Compose v2+, Buildx | **you** (see the OS page) |
| Version manager | [mise](https://mise.jdx.dev) | `make install-deps` |
| Toolchain | Go, golangci-lint, govulncheck, trivy, Helm 4, helm-docs, kubeconform, hadolint, kind, kubectl | `mise install` (from [`.mise.toml`](.mise.toml)) |

## 1. Docker and Docker Compose (installed separately)

Docker is used to build the image, run the compose stacks and the kind cluster,
scan with trivy and validate the Mermaid diagrams. Ubuntu uses Docker Engine;
macOS uses [Colima](https://github.com/abiosoft/colima) (a Linux VM running
Docker Engine, started with `make colima-start`). Install it following your OS
page, then check:

```bash
docker version
docker compose version
docker buildx version
```

## 2. Automatic installation

```bash
git clone https://github.com/aeciopires/docker-hub-image-statistics.git
cd docker-hub-image-statistics
make install-deps
```

`make install-deps` runs [`scripts/install-deps.sh`](scripts/install-deps.sh),
which:

1. detects the OS and architecture and warns on untested platforms;
2. installs the system tools (OS-specific, see the OS page);
3. installs **mise** when missing;
4. trusts [`.mise.toml`](.mise.toml) and runs `mise install`;
5. runs `make check-deps`.

Then activate mise in your shell so the tools are on the `PATH`:

```bash
# bash
echo 'eval "$(mise activate bash)"' >> ~/.bashrc
# zsh (macOS default)
echo 'eval "$(mise activate zsh)"' >> ~/.zshrc
```

The Makefile calls the tools through `mise exec`, so `make` targets work even
without activation.

## 3. Toolchain managed by mise

Pinned in [`.mise.toml`](.mise.toml) (the newest releases when this version was
published):

| Tool | Version | Purpose |
| --- | --- | --- |
| Go | 1.27.4 | Build and test |
| golangci-lint | 2.15.0 | Lint (`make lint`) |
| govulncheck | 1.9.0 | Go vulnerability database (`make vuln`) |
| trivy | 0.76.2 | Image, filesystem and IaC scanning |
| Helm | 4.3.2 | Chart (Helm 4 is required; Helm 3 is not supported) |
| helm-docs | 1.14.2 | Chart README |
| kubeconform | 0.8.0 | Kubernetes manifest validation |
| hadolint | 2.15.1 | Dockerfile lint |
| kind | 0.33.0 | Local Kubernetes cluster (`make kind-up`) |
| kubectl | 1.37.2 | Kubernetes CLI for the kind targets |

Container images used by the project (pulled automatically by Docker):
`golang:1.27.4-trixie`, `gcr.io/distroless/static-debian13:nonroot`,
`grafana/grafana:13.3.1`, `grafana/loki:3.7.10`, `grafana/tempo:3.1.1`,
`prom/prometheus:v3.16.0`, `jaegertracing/jaeger:2.23.0`,
`otel/opentelemetry-collector-contrib:0.165.0`, `minlag/mermaid-cli:12.0.1`,
`kindest/node:v1.37.0`.

## 4. Verify

```bash
make check-deps
```

Every line must show `✔`, including `Helm 4 detected`.

## Hardware and network

- 2 CPU cores and 4 GB RAM for the full observability stack (the application
  alone uses about 30 MB of RAM).
- About 3 GB of disk for images and tools.
- Outbound HTTPS to `hub.docker.com` (the application) and to the mise/Go/Docker
  registries (installation only). The running application downloads nothing
  else: its UI is self-contained.

## Updating the tools

1. Edit the versions in [`.mise.toml`](.mise.toml) and run `mise install`.
2. Update the image versions in [`deploy/compose/compose.observability.yaml`](deploy/compose/compose.observability.yaml),
   the digests in the [`Dockerfile`](Dockerfile) and this page.
3. Run `make check`.

## Troubleshooting

| Symptom | Fix |
| --- | --- |
| `mise: command not found` after install | Open a new terminal or add `~/.local/bin` to `PATH` |
| `make check-deps` shows `Helm 4 is required` | Another Helm is first on `PATH`; activate mise or remove the old Helm |
| `permission denied ... docker.sock` | See the Docker section of your OS page |
| macOS: `Cannot connect to the Docker daemon` | `make colima-start` (or `docker context use colima`) |
| macOS: a bind mount appears empty in a container | Keep the repository under `$HOME` (the only directory Colima shares by default) |
| `make docs-validate` is slow the first time | It downloads the mermaid-cli image once |
