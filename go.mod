module github.com/k8snetworkplumbingwg/sriov-network-operator

go 1.20

require (
	github.com/Masterminds/sprig/v3 v3.2.2
	github.com/blang/semver v3.5.1+incompatible
	github.com/cenkalti/backoff v2.2.1+incompatible
	github.com/coreos/go-systemd/v22 v22.4.0
	github.com/fsnotify/fsnotify v1.6.0
	github.com/golang/glog v1.0.0
	github.com/golang/mock v1.6.0
	github.com/google/go-cmp v0.5.9
	github.com/hashicorp/go-retryablehttp v0.7.1
	github.com/jaypipes/ghw v0.9.0
	github.com/k8snetworkplumbingwg/network-attachment-definition-client v1.4.0
	github.com/k8snetworkplumbingwg/sriov-network-device-plugin v0.0.0-20221127172732-a5a7395122e3
	github.com/onsi/ginkgo/v2 v2.9.5
	github.com/onsi/gomega v1.27.7
	github.com/openshift-kni/k8sreporter v1.0.4
	github.com/openshift/api v0.0.0-20221220162201-efeef9d83325
	github.com/openshift/client-go v0.0.0-20220831193253-4950ae70c8ea
	github.com/openshift/machine-config-operator v0.0.1-0.20230118083703-fc27a2bdaa85
	github.com/pkg/errors v0.9.1
	github.com/spf13/cobra v1.6.1
	github.com/stretchr/testify v1.8.2
	github.com/vishvananda/netlink v1.1.1-0.20211101163509-b10eb8fe5cf6
	github.com/vishvananda/netns v0.0.0-20210104183010-2eb08e3e575f
	go.uber.org/zap v1.24.0
	golang.org/x/time v0.3.0
	gopkg.in/yaml.v2 v2.4.0
	gopkg.in/yaml.v3 v3.0.1
	k8s.io/api v0.28.2
	k8s.io/apiextensions-apiserver v0.27.4
	k8s.io/apimachinery v0.28.2
	k8s.io/client-go v0.27.4
	k8s.io/code-generator v0.27.4
	k8s.io/kubectl v0.27.4
	k8s.io/utils v0.0.0-20230406110748-d93618cff8a2
	sigs.k8s.io/controller-runtime v0.15.2
)

require (
	github.com/coreos/ignition/v2 v2.14.0 // indirect
	github.com/emicklei/go-restful/v3 v3.10.1 // indirect
	github.com/evanphx/json-patch v5.6.0+incompatible // indirect
	github.com/ghodss/yaml v1.0.1-0.20190212211648-25d852aebe32 // indirect
	github.com/google/gnostic v0.6.9 // indirect
	github.com/huandu/xstrings v1.3.2 // indirect
	github.com/inconshreveable/mousetrap v1.1.0 // indirect
	github.com/mattn/go-isatty v0.0.17 // indirect
	github.com/spf13/afero v1.9.3 // indirect
	go.uber.org/atomic v1.10.0 // indirect
	go.uber.org/multierr v1.9.0 // indirect
	golang.org/x/net v0.15.0 // indirect
	google.golang.org/genproto v0.0.0-20221024183307-1bc688fe9f3e // indirect
	k8s.io/gengo v0.0.0-20221011193443-fad74ee6edd9 // indirect
)

replace github.com/emicklei/go-restful => github.com/emicklei/go-restful v2.16.0+incompatible
