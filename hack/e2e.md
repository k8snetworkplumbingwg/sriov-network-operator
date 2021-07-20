# E2E Tests
E2E tests are used to deploy an sriovnetworknodepolicy on an existing Kubernetes cluster and make sure it creates VFs using different configurations.
To run the tests on a Kubernetes cluster run:
```
  ./hack/run-e2e-test.sh
```

# E2E tests using KIND
Kubernetes IN Docker (KIND) is a tool to deploy Kubernetes inside Docker containers. It is used to test multi nodes scenarios on a single baremetal node. 
To run the E2E tests inside a KIND cluster, `./hack/run-e2e-test-kind.sh` can be used. The script deploys a KIND cluster, switch the specified interface to the kind-worker namespace, deploys the operator, and run the E2E tests. There are two modes of operation for the E2E KIND scripts depending on the mechanism used to switch the specified PF into the KIND cluster:
 * `go` mode (default): In this mode, the E2E test suit handle the PF and its VFs switching to the test namespace.
 * `bash_service` mode: In this mode a dedicated bash service is used to switch the PF and VFs to the test namespace.

`kind` tool need to be installed on the server, follow [kind documentation](https://kind.sigs.k8s.io/docs/user/quick-start/) to install kind.

## Running E2E kind tests in `go` mode
To Run the E2E tests in a kind cluster in go mode. In a root shell simply run the E2E kind script:
```
./hack/run-e2e-test-kind.sh <interface pci>
```
## Running the E2E kind tests in `bash_service` mode
The `bash_service` mode uses a bash service to handle the interface switching. To prepare the service, the following needs to be done as root:
```
cp ./hack/vf-netns-switcher.sh /usr/bin/
cp ./hack/vf-switcher.service /etc/systemd/system/
systemctl daemon-reload
```
For the service to work probably the `yq` tool is needed. To install the tool use:
```
wget https://github.com/mikefarah/yq/releases/download/3.4.0/yq_linux_amd64 -O /usr/bin/yq
chmod +x /usr/bin/yq
```

To run the E2E tests do:
```
KUBECONFIG=/etc/kubernetes/admin.conf
INTERFACES_SWITCHER=bash_service
./hack/run-e2e-test-kind.sh <interface pci>
```

## Teardown the KIND cluster
To cleanup the KIND cluster use:
```
./hack/teardown-e2e-kind-cluster.sh
```

