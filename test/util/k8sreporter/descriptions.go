package k8sreporter

import (
	"context"
	"fmt"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	testclient "github.com/k8snetworkplumbingwg/sriov-network-operator/test/util/client"
)

func SriovNetworkNodeStatesSummary(client *testclient.ClientSet, operatorNamespace string) string {
	ret := "SriovNetworkNodeStates:\n"
	nodeStates, err := client.SriovNetworkNodeStates(operatorNamespace).List(context.Background(), metav1.ListOptions{})
	if err != nil {
		return ret + "Summary error: " + err.Error()
	}

	for _, state := range nodeStates.Items {
		ret += fmt.Sprintf("%s\t%s\t%+v\n", state.Name, state.Status.SyncStatus, state.Annotations)
	}

	return ret
}

func Events(client *testclient.ClientSet, namespace string) string {
	ret := fmt.Sprintf("Events in [%s]:\n", namespace)
	events, err := client.Events(namespace).List(context.Background(), metav1.ListOptions{})
	if err != nil {
		return ret + fmt.Sprintf("can't retrieve events for namespace %s: %s", namespace, err.Error())
	}

	for _, item := range events.Items {
		ret += fmt.Sprintf("%s: %s\t%s\t%s\n", item.LastTimestamp, item.Reason, item.InvolvedObject.Name, item.Message)
	}

	return ret
}
