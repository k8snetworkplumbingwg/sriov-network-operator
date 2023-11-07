FROM golang:1.20 AS builder
WORKDIR /go/src/github.com/k8snetworkplumbingwg/sriov-network-operator
COPY . .
RUN make _build-manager BIN_PATH=build/_output/cmd

FROM quay.io/centos/centos:stream9

ARG BUILD_DATE
ARG VERSION
ARG VCS_REF
ARG VCS_BRANCH

LABEL version=$VERSION
LABEL vcs-type="git"
LABEL vcs-branch=$VCS_BRANCH
LABEL vcs-ref=$VCS_REF
LABEL build-date=$BUILD_DATE
LABEL io.k8s.display-name="sriov-network-operator" \
      io.k8s.description="Operator for provisioning and configuring SR-IOV CNI plugin and device plugin"

COPY --from=builder /go/src/github.com/k8snetworkplumbingwg/sriov-network-operator/build/_output/cmd/manager /usr/bin/sriov-network-operator
COPY bindata /bindata
ENV OPERATOR_NAME=sriov-network-operator
CMD ["/usr/bin/sriov-network-operator"]
