# impeller

Manages Helm charts running in Kubernetes clusters.
[![CI-dev-pipeline](https://github.com/target/impeller/actions/workflows/ci-dev-pipeline.yaml/badge.svg)](https://github.com/target/impeller/actions/workflows/ci-dev-pipeline.yaml)
[![Publish Docker image](https://github.com/target/impeller/actions/workflows/docker-image.yml/badge.svg)](https://github.com/target/impeller/actions/workflows/docker-image.yml)
[![Docker Hub](https://img.shields.io/docker/pulls/target/impeller.svg)](https://hub.docker.com/r/target/impeller)
[![Latest Release](https://img.shields.io/github/release/target/impeller.svg)](https://github.com/target/impeller/releases)
[![MIT License](https://img.shields.io/github/license/target/impeller.svg)](https://github.com/target/impeller/blob/master/LICENSE)

## Use Cases
### Managing multiple Helm charts
* Use declarative configurations to specify the versions of Helm charts running in your cluster.
* Easily override chart values and commit your changes to source control.
* Use charts from multiple Helm repos.

### Managing multiple Kubernetes clusters
* Use different charts and different versions in each cluster.
* Share chart overrides across clusters with a `default.yaml` file.
* Make cluster-specific chart overrides when necessary.

### Other features
* Use it as a [Drone](https://drone.io/) plugin for CI/CD.
* Read secrets from environment variables.
* Deploy helm charts with helm/tiller or independently with kubectl

## How to use
### Command line
1. Deployment command:
`impeller --cluster-config-path=./clusters/my-cluster.yaml --kube-config="$(cat ~/.kube/config)" --kube-context my-kubernetes-context`
2. Dry run command:
`impeller --cluster-config-path=./clusters/my-cluster.yaml --kube-config="$(cat ~/.kube/config)" --kube-context my-kubernetes-context --dry-run`
By default override values are hidden with `--dry-run` option. You can add `showValue: true` to your release to enable printout:
```
releases:
  - name: test-release
    namespace: kube-system
    version: ~x.x.x
    overrides:
      - target: global.tag
        showValue: true
        value: 1.6.0
```
3. Diff run command:
`impeller --cluster-config-path=./clusters/my-cluster.yaml --kube-config="$(cat ~/.kube/config)" --kube-context my-kubernetes-context --diff-run`
4. Generate Audit report file:
`impeller --cluster-config-path=./clusters  --audit=true`
or
`impeller --cluster-config-path=./clusters  --audit=true --audit-file=./myreport.csv`

### Drone pipeline
#### Simple example
This example Drone pipeline shows how to manage a single clusters. Updates are automatically deployed on a push/merge to master.

```yaml
deploy-charts:
  when:
    event: push
    branch: master
  image: path-to-docker/image:version
  cluster_config: clusters/my-cluster-name.yaml
  kube_context: my-kubernetes-context
  secrets:
    - source: my-kube-config-drone-secret
      target: KUBE_CONFIG
```

#### Multi-cluster example
This example demonstrates managing multiple clusters with a Drone matrix. Updates will be automatically deployed to test clusters when commit is pushed/merged to master. Production clusters can be deployed to manually by using a `drone deploy` command, allowing additional control over which versions reach production.

```yaml
matrix:
  include:
    - cluster: my-prod-cluster-1
      stage: prod
    - cluster: my-prod-cluster-2
      stage: prod
    - cluster: my-test-cluster-1
      stage: test
    - cluster: my-test-cluster-2
      stage: test

pipeline:
  deploy-charts-prod:
    when:
      event: deployment
      matrix:
        stage: prod
        cluster: ${DRONE_DEPLOY_TO}
    image: path-to-docker/image:version
    cluster_config: clusters/${cluster}.yaml
    kube_context: ${cluster}
    secrets:
      - source: my-kube-config-drone-secret
        target: KUBE_CONFIG

  deploy-charts-test:
    when:
      event: push
      branch: master
    image: path-to-docker/image:version
    cluster_config: clusters/${cluster}.yaml
    kube_context: ${cluster}
    secrets:
      - source: my-kube-config-drone-secret
        target: KUBE_CONFIG
```

## Files and Directory Layout
```
 chart-configs/
 |- clusters/
    |- my-cluster-name.yaml
    |- my-other-cluster-name.yaml
 |- values/
    |- cluster-autoscaler/            # the release name from your cluster file
       |- default.yaml                # overrides for all clusters
       |- my-cluster-name.yaml        # overrides for a specific cluster
       |- my-other-cluster-name.yaml
    |- my-chart/
       |- default.yaml
```

clusters/my-cluster-name.yaml:
```yaml
name: my-cluster-name  # This is used to find cluster-specific override files
helm:
  defaultHistory: 3  # Optional; sets the --history-max flag for the "helm" deployment method on all releases
  log: 5 # specifies log level
  debug: flase # enables debug level logging
  repos:  # Make Helm aware of any repos you want to use
    - name: stable
      url: https://kubernetes-charts.storage.googleapis.com/
    - name: private-repo
      url: https://example.com/my-private-repo/
releases:
  - name: cluster-autoscaler  # Specify the release name
    chartPath: stable/cluster-autoscaler  # Specify the chart source
    namespace: kube-system  # Specify the namespace where to install
    version: 0.7.0  # Specify the version of the chart to install
    deploymentMethod: helm # Specify how the chart should be installed ("helm" or "kubectl")
    history: 3  # Optional; sets the --history-max flag for the "helm" deployment method for this release
  - name: my-chart
    chartPath: private-repo/my-chart
    namespace: kube-system
    version: ~1.x  # Supports the same syntax as Helm's --version flag
    deploymentMethod: kubectl
```

In the above example, the `deploymentMethod` option allows configuration of how Helm charts are deployed. Two methods are available:
* `helm`: This option uses Helm's normal installation method (which is to have the Tiller pod create the resources declared in your chart).
* `kubectl`: If you do not want to run a Tiller pod in your cluster, you can use this option to run `helm template` to convert a chart to Kubernetes manifests and then use `kubectl` to apply that manifest.

values/my-chart/default.yaml:
```yaml
# Place any overrides here, just as you would with Helm.
# This file will be passed as an override to Helm.
resources:
  cpu:
    requests: 100m
    limits: 200m
  memory:
    requests: 1Gi
    limits: 1Gi
```

### Deploying Release from tar file

1. Add `chartsSource` field to the `release` to make impeller download charts tar archive
1. Set `chartPath` to point to extracted chart location.

```
releases:
  - name: istio-base
    namespace: kube-system
    version: ~x.x.x
    chartPath: "./downloads/istio-1.6.0/manifests/charts/base"
    chartsSource: "https://github.com/istio/istio/releases/download/1.6.0/istio-1.6.0-linux-amd64.tar.gz"
```

## Additional examples

### Setup cluster file to setup helm repos only once

You have an option to skip setting up Helm Repo's by setting `SkipSetupHelmRepo` to `true` in the cluster configuration file.
This is useful when you run multiple cluster deployment in parallel and want to configure helm repos only once.
1. Setup file with only helm repos used in all other cluster files, example:
```yaml
name: setup-helm-repos  # This is used to find cluster-specific override files
helm:
  skipSetupHelmRepo: false # Optional;
  defaultHistory: 3  # Optional; sets the --history-max flag for the "helm" deployment method on all releases
  log: 5 # specifies log level
  debug: flase # enables debug level logging
  repos:  # Make Helm aware of any repos you want to use
    - name: stable
      url: https://kubernetes-charts.storage.googleapis.com/
    - name: private-repo
      url: https://example.com/my-private-repo/
releases:
```
2. Set  helm repos used in all other cluster files to `repos: {}`, example:


```yaml
name: my-cluster-name1  # This is used to find cluster-specific override files
helm:
  skipSetupHelmRepo: true # Optional;
  defaultHistory: 3  # Optional; sets the --history-max flag for the "helm" deployment method on all releases
  log: 5 # specifies log level
  debug: flase # enables debug level logging
  repos: {} # Make Helm aware of any repos you want to use
releases:
```

### Override values with environment variables
Override a single value using Helm's `--set` feature.


Add the following release to your cluster YAML file:
```yaml
- name: release-name
  namespace: default
  version: 1.0.0
  chartPath: repo/chart-name
  overrides:
    - target: tls.key
      showValue: false
      valueFrom:
        environment: KEY
```

If you set `showValue` to `true`, the value of the environment variable will logged to `stdout` for debugging purposes. By default, the value is redacted.

### Override values with files
Override a single value from a file using Helm's `--set-file` feature.

Add the following release to your cluster YAML file:
```yaml
- name: release-name
  namespace: default
  version: 1.0.0
  chartPath: repo/chart-name
  overrides:
    - target: tls.key
      valueFrom:
        file: /path/to/key
```

Because the value is not logged, `showValue` has no effect when setting values from file. The file path is always logged to `stdout`.
