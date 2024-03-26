package utils_test

import (
	"testing"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/k8snetworkplumbingwg/sriov-network-operator/pkg/utils"
)

var _ = Describe("HashConfigMap", func() {
	It("should hash the ConfigMap correctly", func() {
		data := make(map[string]string)
		data["key1"] = "value1"
		data["key2"] = "value2"
		cm := &corev1.ConfigMap{
			Data: data,
		}

		expectedHash := "7cb7a94f45100d7dc8aadffbcd409f25"

		actualHash := utils.HashConfigMap(cm)

		Expect(actualHash).To(Equal(expectedHash))
	})

	It("Should not change hash for different resource versions", func() {
		data := make(map[string]string)
		data["key1"] = "value1"
		data["key2"] = "value2"

		cm1 := &corev1.ConfigMap{
			Data: data,
			ObjectMeta: metav1.ObjectMeta{
				ResourceVersion: "68790",
			},
		}

		cm2 := &corev1.ConfigMap{
			Data: data,
			ObjectMeta: metav1.ObjectMeta{
				ResourceVersion: "69889",
			},
		}

		hash1 := utils.HashConfigMap(cm1)
		hash2 := utils.HashConfigMap(cm2)

		Expect(hash1).To(Equal(hash2))
	})

	It("should not change hash for different key orderings", func() {
		data1 := map[string]string{}
		data1["key1"] = "value1"
		data1["key2"] = "value2"
		data2 := map[string]string{}
		data2["key1"] = "value1"
		data2["key2"] = "value2"
		// Collisions in the hashmap _can_ change the order of keys
		data2["key2"] = "value2"

		cm1 := &corev1.ConfigMap{
			Data: data1,
		}

		cm2 := &corev1.ConfigMap{
			Data: data2,
		}

		hash1 := utils.HashConfigMap(cm1)
		hash2 := utils.HashConfigMap(cm2)

		Expect(hash1).To(Equal(hash2))
	})

	It("should parse and process GUIDs correctly", func() {
		guidStr := "00:01:02:03:04:05:06:08"
		nextGuidStr := "00:01:02:03:04:05:06:09"

		guid, err := utils.ParseGUID(guidStr)
		Expect(err).NotTo(HaveOccurred())

		Expect(guid.String()).To(Equal(guidStr))
		Expect((guid + 1).String()).To(Equal(nextGuidStr))
	})
})

func TestUtils(t *testing.T) {
	RegisterFailHandler(Fail)
	RunSpecs(t, "Utils Suite")
}
