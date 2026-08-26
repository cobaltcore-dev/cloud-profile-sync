`core.gardener.cloud_cloudprofiles.yaml` and `core.gardener.cloud_shoots.yaml` files are required for the tests setup (these CRDs should be installed)

Since these resources are not actual CRDs from the k8s api perspective and their spec cant be fetched via `kubectl`

Gardener currently does not provide these CRDs in `yaml` so we need to generate it ourselves as a temporary solution

1. Clone gardener repo
2. Add `kubebuilder` markers to `gardener/pkg/apis/core/v1beta1/types_cloudprofile.go`
```go
// CloudProfile represents certain properties about a provider environment.
// +kubebuilder:object:root=true
// +kubebuilder:resource:scope=Cluster
type CloudProfile struct {
```
3. Generate 
```shell
go run sigs.k8s.io/controller-tools/cmd/controller-gen@latest \
  crd:allowDangerousTypes=true \
  paths=./pkg/apis/core/v1beta1/... \
  output:crd:artifacts:config=crd-out
```
and copy

Probably this thingy should be revisited at some point to reduce overhead. Maybe using different testing approach 
