![build](https://github.com/krkn-chaos/krknctl/actions/workflows/build.yaml/badge.svg)
![test](https://github.com/krkn-chaos/krknctl/actions/workflows/test.yaml/badge.svg)
![coverage](https://krkn-chaos.github.io/krkn-lib-docs/coverage_badge_krknctl.svg)
[![OpenSSF Best Practices](https://www.bestpractices.dev/projects/10651/badge)](https://www.bestpractices.dev/projects/10651)

![krknctl_logo.jpg](media/krknctl_logo.png)

# krknctl
## [krkn](https://github.com/krkn-chaos/krkn) chaos CLI

<br/>
<br/>

> [!CAUTION]  
> __The tool is currently in beta stage, use it at your own risk.__

<br/>

> [!WARNING]
> **krkn-dashboard is deprecated.** If you are looking for a UI to interact with krkn, please use [krkn-operator](https://github.com/krkn-chaos/krkn-operator) instead.

<br/>

## Overview:
`Krknctl` is a tool designed to run and orchestrate [krkn](https://github.com/krkn-chaos/krkn) chaos scenarios utilizing 
container images from the [krkn-hub](https://github.com/krkn-chaos/krkn-hub). 
Its primary objective is to streamline the usage of `krkn` by providing features like:

- Command auto-completion
- Input validation
- Scenario descriptions and detailed instructions

and much more, effectively abstracting the complexities of the container environment. 
This allows users to focus solely on implementing chaos engineering practices without worrying about runtime complexities.

<br/>

## krkn-operator backup and restore

Back up and restore krkn-operator administrative configuration through the
Kubernetes API. The destination cluster must have the operator CRDs installed.

```bash
krknctl operator backup krkn-operator-system
krknctl operator backup krkn-operator-system \
  --kubeconfig ~/.kube/config \
  --output ./backups
krknctl operator restore \
  ./backups/krkn-backup-<timestamp>.tar.gz \
  krkn-operator-system \
  --kubeconfig ~/.kube/config
```

Use `--output` and `--kubeconfig` to override defaults, or use `--kind`
with `--cluster-name` for a KinD cluster. Archives include users, groups,
targets, providers, and operator-managed credentials; cloud credentials, Files
page data, runtime status, and the JWT signing Secret are excluded. Treat
archives as sensitive files and do not commit them to source control.

<br/>

## Documentation:

Instructions on how to setup, configure and run Kraken can be found in the [documentation](https://krkn-chaos.dev/docs/krknctl/).
