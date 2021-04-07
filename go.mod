module github.com/k8snetworkplumbingwg/sriov-network-operator

go 1.15

require (
	cloud.google.com/go v0.58.0 // indirect
	github.com/Masterminds/semver v1.5.0 // indirect
	github.com/Masterminds/sprig v2.22.0+incompatible
	github.com/blang/semver v3.5.1+incompatible
	github.com/cenkalti/backoff v2.2.1+incompatible
	github.com/coreos/go-systemd/v22 v22.0.0
	github.com/fsnotify/fsnotify v1.4.9
	github.com/go-logr/logr v0.3.0
	github.com/gogo/protobuf v1.3.2 // indirect
	github.com/golang/glog v0.0.0-20160126235308-23def4e6c14b
	github.com/google/go-cmp v0.5.2
	github.com/huandu/xstrings v1.3.2 // indirect
	github.com/intel/sriov-network-device-plugin v0.0.0-20200924101303-b7f6d3e06797
	github.com/jaypipes/ghw v0.6.1
	github.com/k8snetworkplumbingwg/network-attachment-definition-client v0.0.0-20200626054723-37f83d1996bc
	github.com/mitchellh/copystructure v1.0.0 // indirect
	github.com/onsi/ginkgo v1.14.1
	github.com/onsi/gomega v1.10.2
	github.com/openshift/client-go v0.0.0-20200827190008-3062137373b5
	github.com/openshift/machine-config-operator v0.0.1-0.20201023110058-6c8bd9b2915c
	github.com/pkg/errors v0.9.1
	github.com/spf13/cobra v1.1.1
	github.com/vishvananda/netlink v1.1.0
	golang.org/x/time v0.0.0-20200630173020-3af7569d3a1e
	gopkg.in/yaml.v2 v2.3.0
	k8s.io/api v0.20.5
	k8s.io/apimachinery v0.20.5
	k8s.io/client-go v0.20.5
	k8s.io/code-generator v0.20.5
	k8s.io/kubectl v0.20.5
	k8s.io/utils v0.0.0-20210111153108-fddb29f9d009
	sigs.k8s.io/controller-runtime v0.8.3
)

replace (
	github.com/gogo/protobuf => github.com/gogo/protobuf v1.3.2
	github.com/openshift/api => github.com/openshift/api v0.0.0-20200827090112-c05698d102cf
	k8s.io/kubectl => k8s.io/kubectl v0.20.5
	k8s.io/kubelet => k8s.io/kubelet v0.20.5
)
