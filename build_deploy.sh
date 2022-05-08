make generate
make manifests
make install

export IMG=docker.io/navadiaev/marvell-sriov-operator:latest
export NAMESPACE=sriov-network-operator

cd config/manager
kustomize edit set image controller=${IMG}
kustomize edit set namespace "${NAMESPACE}"
cd ../../

cd config/default
kustomize edit set namespace "${NAMESPACE}"
cd ../../

#build and push
docker build -f Dockerfile -t "navadiaev/marvell-sriov-operator" .
docker push navadiaev/marvell-sriov-operator:latest

#deploy to operator
make deploy-setup

