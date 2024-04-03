package store

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sync"

	"github.com/google/renameio/v2"
	"sigs.k8s.io/controller-runtime/pkg/log"

	sriovnetworkv1 "github.com/k8snetworkplumbingwg/sriov-network-operator/api/v1"
	"github.com/k8snetworkplumbingwg/sriov-network-operator/pkg/consts"
	"github.com/k8snetworkplumbingwg/sriov-network-operator/pkg/utils"
)

// Store interface provides methods to store and query information
// about OVS bridges that are managed by the operator
//
//go:generate ../../../../../../bin/mockgen -destination mock/mock_store.go -source store.go
type Store interface {
	// GetManagedOVSBridges returns map with saved information about managed OVS bridges.
	// Bridge name is a key in the map
	GetManagedOVSBridges() map[string]*sriovnetworkv1.OVSConfigExt
	// GetManagedOVSBridge returns saved information about managed OVS bridge
	GetManagedOVSBridge(name string) *sriovnetworkv1.OVSConfigExt
	// AddManagedOVSBridge save information about the OVS bridge
	AddManagedOVSBridge(br *sriovnetworkv1.OVSConfigExt) error
	// RemoveManagedOVSBridge removes saved information about the OVS bridge
	RemoveManagedOVSBridge(name string) error
}

// New returns default implementation of Store interfaces
func New() (Store, error) {
	s := &ovsStore{
		lock: &sync.RWMutex{},
	}
	var err error
	err = s.ensureStoreDirExist()
	if err != nil {
		return nil, err
	}
	s.cache, err = s.readStoreFile()
	if err != nil {
		return nil, err
	}
	return s, nil
}

type ovsStore struct {
	lock  *sync.RWMutex
	cache map[string]sriovnetworkv1.OVSConfigExt
}

// GetManagedOVSBridges returns map with saved information about managed OVS bridges.
// Bridge name is a key in the map
func (s *ovsStore) GetManagedOVSBridges() map[string]*sriovnetworkv1.OVSConfigExt {
	funcLog := log.Log
	funcLog.V(2).Info("GetManagedOVSBridges(): get information about all managed OVS bridges from the store")
	s.lock.RLock()
	defer s.lock.RUnlock()
	result := make(map[string]*sriovnetworkv1.OVSConfigExt, len(s.cache))
	for k, v := range s.cache {
		result[k] = v.DeepCopy()
	}
	if funcLog.V(2).Enabled() {
		data, _ := json.Marshal(result)
		funcLog.V(2).Info("GetManagedOVSBridges()", "result", string(data))
	}
	return result
}

// GetManagedOVSBridge returns saved information about managed OVS bridge
func (s *ovsStore) GetManagedOVSBridge(name string) *sriovnetworkv1.OVSConfigExt {
	funcLog := log.Log.WithValues("name", name)
	funcLog.V(2).Info("GetManagedOVSBridge(): get information about managed OVS bridge from the store")
	s.lock.RLock()
	defer s.lock.RUnlock()
	b, found := s.cache[name]
	if !found {
		funcLog.V(2).Info("GetManagedOVSBridge(): bridge info not found")
		return nil
	}
	if funcLog.V(2).Enabled() {
		data, _ := json.Marshal(&b)
		funcLog.V(2).Info("GetManagedOVSBridge()", "result", string(data))
	}
	return b.DeepCopy()
}

// AddManagedOVSBridge save information about the OVS bridge
func (s *ovsStore) AddManagedOVSBridge(br *sriovnetworkv1.OVSConfigExt) error {
	log.Log.V(2).Info("AddManagedOVSBridge(): add information about managed OVS bridge to the store", "name", br.Name)
	s.lock.Lock()
	defer s.lock.Unlock()
	s.cache[br.Name] = *br.DeepCopy()
	return s.writeStoreFile()
}

// RemoveManagedOVSBridge removes saved information about the OVS bridge
func (s *ovsStore) RemoveManagedOVSBridge(name string) error {
	log.Log.V(2).Info("RemoveManagedOVSBridge(): remove information about managed OVS bridge from the store", "name", name)
	s.lock.Lock()
	defer s.lock.Unlock()
	delete(s.cache, name)
	return s.writeStoreFile()
}

func (s *ovsStore) ensureStoreDirExist() error {
	storeDir := filepath.Dir(s.getStoreFilePath())
	_, err := os.Stat(storeDir)
	if err != nil {
		if os.IsNotExist(err) {
			err = os.MkdirAll(storeDir, os.ModeDir)
			if err != nil {
				return fmt.Errorf("failed to create directory for store %s: %v", storeDir, err)
			}
		} else {
			return fmt.Errorf("failed to check if directory for store exist %s: %v", storeDir, err)
		}
	}
	return nil
}

func (s *ovsStore) readStoreFile() (map[string]sriovnetworkv1.OVSConfigExt, error) {
	storeFilePath := s.getStoreFilePath()
	funcLog := log.Log.WithValues("storeFilePath", storeFilePath)
	funcLog.V(2).Info("readStoreFile(): read OVS store file")
	result := map[string]sriovnetworkv1.OVSConfigExt{}
	data, err := os.ReadFile(storeFilePath)
	if err != nil {
		if os.IsNotExist(err) {
			funcLog.V(2).Info("readStoreFile(): OVS store file not found")
			return result, nil
		}
		return nil, err
	}
	if err := json.Unmarshal(data, &result); err != nil {
		funcLog.Error(err, "readStoreFile(): failed to unmarshal content of the OVS store file")
		return nil, err
	}
	return result, nil
}

func (s *ovsStore) writeStoreFile() error {
	storeFilePath := s.getStoreFilePath()
	funcLog := log.Log.WithValues("storeFilePath", storeFilePath)
	data, err := json.Marshal(s.cache)
	if err != nil {
		funcLog.Error(err, "writeStoreFile(): can't serialize cached info about managed OVS bridges")
		return err
	}
	if err := renameio.WriteFile(storeFilePath, data, 0644); err != nil {
		funcLog.Error(err, "writeStoreFile(): can't write info about managed OVS bridge to disk")
		return err
	}
	return nil
}

func (s *ovsStore) getStoreFilePath() string {
	return utils.GetHostExtensionPath(consts.ManagedOVSBridgesPath)
}
