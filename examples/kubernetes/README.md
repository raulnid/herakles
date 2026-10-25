# herakles on Kubernetes

A reproducible demo: herakles probing the eurystheus mock API in a local
[kind](https://kind.sigs.k8s.io/) or [k3d](https://k3d.io/) cluster.

```sh
kind create cluster --name herakles          # or: k3d cluster create herakles
kubectl apply -k examples/kubernetes
kubectl -n herakles-demo rollout status deploy/herakles
kubectl -n herakles-demo logs -f deploy/herakles
```

Metrics:

```sh
kubectl -n herakles-demo port-forward svc/herakles 9090 &
curl -s localhost:9090/metrics | grep '^herakles_labor_success'
```

## Images built locally

To try unreleased changes, build both images and load them into the cluster:

```sh
docker build --target herakles   -t ghcr.io/raulnid/herakles:dev .
docker build --target eurystheus -t ghcr.io/raulnid/herakles-eurystheus:dev .
kind load docker-image ghcr.io/raulnid/herakles:dev ghcr.io/raulnid/herakles-eurystheus:dev --name herakles
cd examples/kubernetes && kustomize edit set image ghcr.io/raulnid/herakles:dev ghcr.io/raulnid/herakles-eurystheus:dev
```

## Prometheus Operator

With [kube-prometheus-stack](https://github.com/prometheus-community/helm-charts/tree/main/charts/kube-prometheus-stack)
installed, add the label your Prometheus selects on and apply the
ServiceMonitor and the example alerts:

```sh
kubectl apply -f examples/kubernetes/monitoring/
```

## What the manifests do

| File | Purpose |
|---|---|
| `herakles.yaml` | The probe config, mounted through a generated ConfigMap. Editing it rolls the pod. |
| `secret.yaml` | Example API key, mounted as a file. Use your secret store in real clusters. |
| `deployment.yaml` | One replica with `Recreate`: two instances would run mutating steps concurrently. Non-root, read-only root filesystem, no capabilities, no service account token. |
| `service.yaml` | Exposes `:9090` for scraping. |
| `eurystheus.yaml` | The demo API. Replace it with your own service. |
| `monitoring/` | ServiceMonitor and PrometheusRule (needs the operator CRDs). |
